// Command agent is the Kraken node daemon: a gRPC server (one per host) that the
// Panel drives over mutual TLS to install, run, and observe game-server
// containers via Docker. This skeleton serves the NodeService backed by an
// in-memory fake runtime; the Docker-backed runtime and mTLS are forthcoming.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/briggleman/kraken/internal/agent"
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

	// Mutual TLS when certs are configured; plaintext (dev) otherwise.
	var grpcServer *grpc.Server
	secure := false
	cert, key, ca := env("KRAKEN_TLS_CERT", ""), env("KRAKEN_TLS_KEY", ""), env("KRAKEN_TLS_CA", "")
	if cert != "" && key != "" && ca != "" {
		tlsCfg, terr := mtls.ServerTLS(cert, key, ca)
		if terr != nil {
			return fmt.Errorf("load server TLS: %w", terr)
		}
		grpcServer = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
		secure = true
	} else {
		grpcServer = grpc.NewServer()
	}
	agentpb.RegisterNodeServiceServer(grpcServer, agent.NewService(rt))

	// SFTP server for power-user file access — a separate SSH listener that
	// chroots each per-server login to that server's data dir. No-op on the fake
	// runtime. The host key persists so the server's identity is stable.
	sftpAddr := env("KRAKEN_SFTP_ADDR", ":2022")
	hostKeyPath := env("KRAKEN_SFTP_HOST_KEY", "sftp_host_key")
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
