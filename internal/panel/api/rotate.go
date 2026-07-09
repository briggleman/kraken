package api

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

const (
	// rotateBefore is the remaining-validity threshold that triggers rotation.
	// Agent certs live 90 days (mtls.DefaultAgentCertTTL), so rotation fires
	// around day 60 — leaving a wide window to notice a persistent failure.
	rotateBefore = 30 * 24 * time.Hour
	// rotateRetryInterval throttles rotation attempts per node so a failing
	// rotation doesn't retry on every 4s reconcile tick.
	rotateRetryInterval = time.Hour
)

// maybeRotateAgentCert rotates a node's agent cert when it nears expiry.
// Driven from reconcileNode on the already-authenticated mTLS channel: the
// agent mints a fresh key + CSR (BeginCertRotation), the Panel signs it with
// the enrollment CA, and the agent verifies + hot-swaps it
// (CompleteCertRotation). Best-effort: failures are logged and retried on a
// later reconcile, throttled per node.
func (s *Server) maybeRotateAgentCert(ctx context.Context, n *cluster.Node, client agentpb.NodeServiceClient, info *agentpb.NodeInfo) {
	if s.caCert == nil || s.caKey == nil || info.CertNotAfterUnix == 0 {
		return // no signing CA, or the agent isn't serving TLS
	}
	exp := time.Unix(info.CertNotAfterUnix, 0)
	if time.Until(exp) > rotateBefore {
		return
	}
	s.rotateMu.Lock()
	if last, ok := s.lastRotate[n.ID]; ok && time.Since(last) < rotateRetryInterval {
		s.rotateMu.Unlock()
		return
	}
	s.lastRotate[n.ID] = time.Now()
	s.rotateMu.Unlock()

	s.logger.Info("agent cert nearing expiry — rotating", "node", n.Name,
		"not_after", exp.UTC().Format(time.RFC3339))
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	begin, err := client.BeginCertRotation(cctx, &agentpb.BeginCertRotationRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			s.logger.Warn("cert rotation: agent does not support rotation — upgrade the agent binary", "node", n.Name)
			return
		}
		s.logger.Warn("cert rotation: begin failed", "node", n.Name, "err", err)
		return
	}
	certPEM, err := mtls.SignAgentCSR(s.caCert, s.caKey, []byte(begin.Csr), mtls.DefaultAgentCertTTL)
	if err != nil {
		s.logger.Warn("cert rotation: CSR signing failed", "node", n.Name, "err", err)
		return
	}
	done, err := client.CompleteCertRotation(cctx, &agentpb.CompleteCertRotationRequest{Certificate: string(certPEM)})
	if err != nil {
		s.logger.Warn("cert rotation: install failed", "node", n.Name, "err", err)
		return
	}
	s.logger.Info("agent cert rotated", "node", n.Name,
		"new_not_after", time.Unix(done.NotAfterUnix, 0).UTC().Format(time.RFC3339),
		"issued_cert", mtls.SummarizePEM(certPEM))
}
