// Command agent is the Kraken node daemon: a gRPC server (one per host) that the
// Panel drives over mutual TLS to install, run, and observe game-server
// containers via Docker. This skeleton serves the NodeService backed by an
// in-memory fake runtime; the Docker-backed runtime and mTLS are forthcoming.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/agent/enroll"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
	"github.com/briggleman/kraken/internal/shared/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("kraken-agent", version.String())
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("agent exited with error", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := env("KRAKEN_AGENT_ADDR", ":9090")
	nodeID := env("KRAKEN_NODE_ID", "abyss-node-01")
	nodeOS := env("KRAKEN_NODE_OS", "linux")
	wine := env("KRAKEN_NODE_WINE", "true") == "true"

	// KRAKEN_STATE_DIR groups Agent-owned state (mTLS bundle, SFTP host key,
	// … more later) under one directory so systemd / containers point at
	// /var/lib/kraken and get sensible defaults for everything. Legacy
	// default is cwd-relative so existing dev setups keep working unchanged.
	stateDir := env("KRAKEN_STATE_DIR", ".")

	// Resolve mTLS material up-front — needed for the safety guard below and
	// then again to configure the gRPC server. When all three are set the
	// Panel↔Agent channel is mutually authenticated; otherwise the server
	// accepts plaintext connections and the NodeService is effectively
	// unauthenticated. Plaintext + a non-loopback listen address = anyone
	// with LAN reach can drive the Agent's docker socket, so we refuse.
	cert, key, ca := env("KRAKEN_TLS_CERT", ""), env("KRAKEN_TLS_KEY", ""), env("KRAKEN_TLS_CA", "")
	secure := cert != "" && key != "" && ca != ""

	// Auto-enroll: if TLS isn't configured but KRAKEN_PANEL_URL is set,
	// enroll with the Panel over its loopback-gated /setup/local-enroll →
	// /agents/enroll flow. The persisted cert bundle survives across
	// restarts (subsequent boots reuse it without contacting the Panel).
	// A separate host / non-quickstart operator sets KRAKEN_TLS_* directly
	// (via `krakenctl enroll`) and this branch is skipped entirely.
	if !secure {
		if panelURL := env("KRAKEN_PANEL_URL", ""); panelURL != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			paths, aerr := enroll.EnsureCerts(ctx, panelURL, stateDir, nil, 90*time.Second, logger)
			cancel()
			if aerr != nil {
				return fmt.Errorf("auto-enroll with Panel at %s: %w", panelURL, aerr)
			}
			cert, key, ca = paths.Cert, paths.Key, paths.CA
			secure = true
		}
	}

	if secure {
		logTLSBundle(logger, cert, ca)
	}

	if !secure && !isLoopbackAddr(addr) && env("KRAKEN_ALLOW_INSECURE_GRPC", "") != "1" {
		return fmt.Errorf("agent: refusing to serve plaintext gRPC on non-loopback address %q — "+
			"enroll with the Panel (set KRAKEN_PANEL_URL for auto-enroll, or run `krakenctl enroll` to populate KRAKEN_TLS_CERT/KEY/CA), "+
			"bind loopback with KRAKEN_AGENT_ADDR=127.0.0.1:9090, "+
			"or opt in explicitly with KRAKEN_ALLOW_INSECURE_GRPC=1", addr)
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Select the container backend: Docker by default, the in-memory fake when
	// KRAKEN_RUNTIME=fake or the Docker daemon is unreachable.
	rt := selectRuntime(logger, nodeID, nodeOS, wine)
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
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 {
				logger.Info("mTLS: client authenticated", "peer", mtls.SummarizeCert(cs.PeerCertificates[0]))
			}
			return nil
		}
		grpcServer = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		grpcServer = grpc.NewServer()
	}
	agentpb.RegisterNodeServiceServer(grpcServer, agent.NewService(rt, svcOpts...))

	// SFTP server for power-user file access — a separate SSH listener that
	// chroots each per-server login to that server's data dir. No-op on the
	// fake runtime. The host key persists so the server's identity is stable.
	sftpAddr := env("KRAKEN_SFTP_ADDR", ":2022")
	hostKeyPath := env("KRAKEN_SFTP_HOST_KEY", filepath.Join(stateDir, "sftp_host_key"))
	if sftpSrv, serr := agent.StartSFTP(rt, sftpAddr, hostKeyPath, logger); serr != nil {
		logger.Warn("SFTP server not started", "err", serr)
	} else if sftpSrv != nil {
		logger.Info("SFTP server listening", "addr", sftpAddr)
		defer func() { _ = sftpSrv.Close() }()
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

// selectRuntime returns the Docker runtime unless KRAKEN_RUNTIME=fake or the
// Docker daemon cannot be reached, in which case it falls back to the fake.
func selectRuntime(logger *slog.Logger, nodeID, nodeOS string, wine bool) agent.Runtime {
	if env("KRAKEN_RUNTIME", "docker") == "fake" {
		logger.Warn("using fake runtime (KRAKEN_RUNTIME=fake)")
		return agent.NewFakeRuntime(nodeID, nodeOS, wine, version.Version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drt, err := agent.NewDockerRuntime(ctx, nodeID, wine, version.Version)
	if err != nil {
		logger.Warn("Docker unavailable; falling back to fake runtime", "err", err)
		return agent.NewFakeRuntime(nodeID, nodeOS, wine, version.Version)
	}
	logger.Info("using Docker runtime")
	return drt
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
