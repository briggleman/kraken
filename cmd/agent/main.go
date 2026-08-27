// Command agent is the Kraken node daemon: a gRPC server (one per host) that the
// Panel drives over mutual TLS to install, run, and observe game-server
// containers via Docker. This skeleton serves the NodeService backed by an
// in-memory fake runtime; the Docker-backed runtime and mTLS are forthcoming.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/agent/config"
	"github.com/briggleman/kraken/internal/agent/enroll"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
	"github.com/briggleman/kraken/internal/shared/version"
)

func main() {
	cfg, modes, err := config.Load(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if modes.ShowVersion {
		fmt.Println("kraken-agent", version.String())
		return
	}
	if modes.PrintConfig {
		fmt.Print(cfg.YAML())
		return
	}

	// Windows service control (--service install/uninstall/start/stop) and
	// SCM-launched runs. Each platform owns its own branch — and exits — so
	// the non-Windows build carries no always-erroring stubs.
	if serviceMain(cfg, modes.Service) {
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(context.Background(), logger, cfg); err != nil {
		logger.Error("agent exited with error", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, cfg *config.Config) error {
	// Components that still read KRAKEN_* directly (the Docker runtime's data,
	// backup, and isolation settings) must see the same values resolved here.
	if err := cfg.Export(); err != nil {
		return err
	}
	if cfg.ConfigFile != "" {
		logger.Info("configuration loaded", "file", cfg.ConfigFile, "root", cfg.Root)
	}

	addr, nodeID, nodeOS := cfg.Addr, cfg.NodeID, cfg.NodeOS

	// Resolve mTLS material up-front — needed for the safety guard below and
	// then again to configure the gRPC server. When all three are set the
	// Panel↔Agent channel is mutually authenticated; otherwise the server
	// accepts plaintext connections and the NodeService is effectively
	// unauthenticated. Plaintext + a non-loopback listen address = anyone
	// with LAN reach can drive the Agent's docker socket, so we refuse.
	cert, key, ca := cfg.TLSCert, cfg.TLSKey, cfg.TLSCA
	secure := cfg.Secure()

	// Auto-enroll: if TLS isn't configured but a Panel URL is, enroll with the
	// Panel and persist the bundle (subsequent boots reuse it without contacting
	// the Panel). With an enroll token (minted in the Panel's Add Node dialog)
	// this works from any host that can reach the Panel; without one it falls
	// back to the loopback-gated /setup/local-enroll flow for a co-located
	// Agent. An operator who enrolled by hand (via `krakenctl enroll`, or by
	// dropping the bundle under <root>/certs) has TLS configured already and
	// skips this branch entirely.
	if !secure && cfg.PanelURL != "" {
		opts := enroll.Options{
			Token:         cfg.EnrollToken,
			CAFingerprint: cfg.CAFingerprint,
			AgentPort:     listenPort(addr),
			Deadline:      90 * time.Second,
		}
		if cfg.EnrollToken != "" {
			// Remote enroll: bake this host's dialable IPs into the cert so
			// the Panel can prefill the node's registration address.
			opts.ExtraHosts = enroll.LocalIPs()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		paths, aerr := enroll.EnsureCerts(ctx, cfg.PanelURL, cfg.StateDir, opts, logger)
		cancel()
		if aerr != nil {
			if cfg.EnrollToken != "" {
				return fmt.Errorf("auto-enroll with Panel at %s: %w — bootstrap tokens are one-time and short-lived (and a Panel restart invalidates them); mint a fresh one in the Panel's Add Node dialog and retry", cfg.PanelURL, aerr)
			}
			return fmt.Errorf("auto-enroll with Panel at %s: %w", cfg.PanelURL, aerr)
		}
		cert, key, ca = paths.Cert, paths.Key, paths.CA
		secure = true
	} else if secure && cfg.EnrollToken != "" {
		logger.Info("enroll token ignored: an mTLS bundle is already configured (delete it to re-enroll)")
	}

	if secure {
		logTLSBundle(logger, cert, ca)
	}

	if !secure && !isLoopbackAddr(addr) && !cfg.InsecureGRPCAllowed() {
		return fmt.Errorf("agent: refusing to serve plaintext gRPC on non-loopback address %q — "+
			"enroll with the Panel (--panel-url for auto-enroll, or run `krakenctl enroll` and point --tls-cert/--tls-key/--tls-ca at the bundle; "+
			"a bundle under <root>/certs is picked up automatically), "+
			"bind loopback with --addr 127.0.0.1:9090, "+
			"or opt in explicitly with --allow-insecure-grpc", addr)
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Self-update wiring. The boot check runs before anything serves: if a
	// freshly-updated binary has burned through its start attempts without
	// reaching the health milestone, the updater swaps the previous binary
	// back and we restart straight into it.
	updater, uerr := agent.NewSelfUpdater(version.Version, cfg.StateDir, restartAgent, logger)
	if uerr != nil {
		logger.Warn("self-update unavailable", "err", uerr)
	} else if updater.CheckBoot() {
		updater.Restart() // does not return
	}

	// Select the container backend: Docker by default, the in-memory fake when
	// the runtime is set to "fake" or the Docker daemon is unreachable.
	rt := selectRuntime(logger, cfg)
	if closer, ok := rt.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}

	// Build the gRPC server per the resolution above. Under TLS the serving
	// cert is owned by a CertManager so the Panel-driven rotation RPCs can
	// hot-swap it without a restart.
	var grpcServer *grpc.Server
	var svcOpts []agent.ServiceOption
	if secure {
		tlsCfg, terr := mtls.ServerTLS(cert, key, ca)
		if terr != nil {
			return fmt.Errorf("load server TLS: %w", terr)
		}
		cm, cerr := agent.NewCertManager(cert, key, ca, logger)
		if cerr != nil {
			return fmt.Errorf("init cert manager: %w", cerr)
		}
		tlsCfg.Certificates = nil
		tlsCfg.GetCertificate = cm.GetCertificate
		svcOpts = append(svcOpts, agent.WithCertManager(cm))
		// Debug visibility into handshakes: an "attempt" line with no matching
		// "client authenticated" line means client-cert verification failed
		// (the client side logs the specific x509 reason).
		tlsCfg.GetConfigForClient = func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
			logger.Info("mTLS: handshake attempt", "remote", hi.Conn.RemoteAddr().String(), "sni", hi.ServerName)
			return nil, nil
		}
		// Chain the identity enforcement ServerTLS installed (only the Panel may
		// connect) so the logging hook adds to it rather than replacing it — a
		// bare `return nil` here would silently re-open the listener to any peer
		// Agent's cert.
		enforce := tlsCfg.VerifyConnection
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if err := enforce(cs); err != nil {
				if len(cs.PeerCertificates) > 0 {
					logger.Warn("mTLS: client REJECTED — not the Panel", "peer", mtls.SummarizeCert(cs.PeerCertificates[0]), "err", err)
				}
				return err
			}
			if len(cs.PeerCertificates) > 0 {
				logger.Info("mTLS: client authenticated", "peer", mtls.SummarizeCert(cs.PeerCertificates[0]))
			}
			return nil
		}
		grpcServer = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		grpcServer = grpc.NewServer()
	}
	if updater != nil {
		svcOpts = append(svcOpts, agent.WithSelfUpdater(updater))
	}
	svc := agent.NewService(rt, svcOpts...)
	agentpb.RegisterNodeServiceServer(grpcServer, svc)

	// Reverse-tunnel mode: dial out to the Panel and serve the same NodeService
	// over the tunnel, so this node needs no inbound gRPC port. The local TCP
	// listener stays up alongside it — direct and tunnel are not exclusive, and
	// keeping it means an operator can flip a node between modes without
	// touching the agent.
	if cfg.TunnelEnabled() {
		if !secure {
			return fmt.Errorf("agent: tunnel mode requires an mTLS bundle — enroll with the Panel first (--panel-url, or --enroll-token from the Add Node dialog)")
		}
		tunnelAddr, terr := cfg.ResolveTunnelAddr()
		if terr != nil {
			return terr
		}
		tc, terr := agent.NewTunnelClient(tunnelAddr, cert, key, ca, cfg.SFTPAddr, svc, logger)
		if terr != nil {
			return fmt.Errorf("agent: tunnel client: %w", terr)
		}
		logger.Info("tunnel mode: dialing out to the Panel (no inbound gRPC port needed)", "panel", tunnelAddr)
		go tc.Run(ctx)
	}

	// SFTP server for power-user file access — a separate SSH listener that
	// chroots each per-server login to that server's data dir. No-op on the
	// fake runtime. The host key persists so the server's identity is stable.
	sftpAddr := cfg.SFTPAddr
	if sftpSrv, serr := agent.StartSFTP(rt, sftpAddr, cfg.SFTPHostKey, logger); serr != nil {
		logger.Warn("SFTP server not started", "err", serr)
	} else if sftpSrv != nil {
		logger.Info("SFTP server listening", "addr", sftpAddr)
		defer func() { _ = sftpSrv.Close() }()
	}

	// Uptime-based fallback for the update health milestone (the primary
	// milestone is the Panel's first NodeInfo poll).
	if updater != nil {
		updater.StartHealthTimer()
	}

	errCh := make(chan error, 1)
	go func() {
		if secure {
			logger.Info("agent serving with mutual TLS", "addr", addr, "node", nodeID, "os", nodeOS)
		} else {
			logger.Warn("agent serving WITHOUT mTLS (dev mode)", "addr", addr, "node", nodeID, "os", nodeOS)
		}
		if err := grpcServer.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return fmt.Errorf("grpc serve: %w", err)
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
		grpcServer.GracefulStop()
	case <-ctx.Done():
		logger.Info("shutting down", "reason", "service stop requested")
		grpcServer.GracefulStop()
	}
	return nil
}

// logTLSBundle logs the identity of the mTLS material the Agent is about to
// serve with, and cross-checks the cert against the CA it will trust for
// client auth. A stale bundle — enrolled under a CA the Panel no longer uses,
// or expired — shows up here as an explicit warning instead of a stream of
// opaque handshake failures later.
func logTLSBundle(logger *slog.Logger, certFile, caFile string) {
	certPEM, cerr := os.ReadFile(certFile)
	caPEM, kerr := os.ReadFile(caFile)
	if cerr != nil || kerr != nil {
		logger.Warn("mTLS: could not read bundle for inspection", "cert_err", cerr, "ca_err", kerr)
		return
	}
	logger.Info("mTLS: agent certificate", "file", certFile, "cert", mtls.SummarizePEM(certPEM))
	logger.Info("mTLS: trusted CA (client certs must chain to this)", "file", caFile, "ca", mtls.SummarizePEM(caPEM))
	if err := mtls.VerifyPEM(certPEM, caPEM); err != nil {
		logger.Warn("mTLS: agent cert does NOT verify against the bundled CA — "+
			"Panel connections will fail the handshake; delete the bundle and re-enroll this agent",
			"err", err)
	} else {
		logger.Info("mTLS: agent cert verifies against bundled CA")
	}
}

// listenPort extracts the port from a listen address like ":9090" or
// "192.168.0.75:9091"; 9090 when unparseable.
func listenPort(addr string) int {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return 9090
}

// isLoopbackAddr reports whether the host part of a listen address binds to
// loopback only. Empty host or 0.0.0.0/:: means the Agent accepts LAN
// traffic and is treated as non-loopback so the plaintext-gRPC guard fires.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// selectRuntime returns the Docker runtime, or the in-memory fake when the
// operator asked for one explicitly.
//
// An unreachable Docker daemon is no longer a reason to fall back: the fake
// reports plenty of memory and happily "runs" servers, so a node with a stopped
// Docker Desktop looked healthy and accepted placements that went nowhere. The
// Docker runtime instead comes up degraded, reports its runtime as unavailable in
// NodeInfo (the Panel shows the node as partial and won't schedule onto it), and
// recovers on its own when the daemon returns.
func selectRuntime(logger *slog.Logger, cfg *config.Config) agent.Runtime {
	wine := cfg.WineEnabled()
	if cfg.Runtime == "fake" {
		logger.Warn("using fake runtime (runtime=fake)")
		return agent.NewFakeRuntime(cfg.NodeID, cfg.NodeOS, wine, version.Version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drt, err := agent.NewDockerRuntime(ctx, cfg.NodeID, cfg.NodeOS, wine, version.Version)
	if err != nil {
		// Only a client that can't be constructed at all lands here (e.g. a
		// malformed DOCKER_HOST); an unreachable daemon does not.
		logger.Error("could not initialize the Docker runtime; falling back to the fake runtime — this node cannot run game servers",
			"err", err)
		return agent.NewFakeRuntime(cfg.NodeID, cfg.NodeOS, wine, version.Version)
	}
	if ok, rerr := drt.RuntimeHealth(); !ok {
		logger.Warn("Docker daemon unreachable — serving in a degraded state; the Panel will show this node as partial. "+
			"Start Docker and the Agent picks it up on its own (no restart needed)", "err", rerr)
	} else {
		logger.Info("using Docker runtime", "mode", drt.OSType())
	}
	return drt
}

// stripServiceFlag returns args minus any --service/-service flag (both
// "--service install" and "--service=install" spellings). Used at service
// install time: the registered command line is the install invocation with
// the control flag removed, so configuration flags carry over verbatim.
func stripServiceFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.TrimLeft(a, "-")
		if name == "service" && strings.HasPrefix(a, "-") {
			i++ // skip the value too
			continue
		}
		if strings.HasPrefix(a, "-") && strings.HasPrefix(name, "service=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// joinArgs renders an arg list for display, quoting anything with spaces.
func joinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			quoted[i] = strconv.Quote(a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
