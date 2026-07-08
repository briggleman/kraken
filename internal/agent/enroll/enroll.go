// Package enroll implements the Agent's auto-enrollment client — the
// counterpart to the Panel's /setup/local-enroll + /agents/enroll endpoints.
// It's used at Agent startup when no TLS material is configured, so a fresh
// single-host install ends up on mutual TLS without operator intervention.
//
// The flow mirrors what `krakenctl enroll` does interactively:
//
//  1. POST /api/v1/setup/local-enroll on the co-located Panel (which
//     enforces a loopback source-IP check) → one-time bootstrap token
//  2. Generate an Agent key + CSR via internal/shared/mtls
//  3. POST /api/v1/agents/enroll with {token, csr} → signed cert + CA
//  4. Persist agent.pem / agent-key.pem / ca.pem under the state dir
//
// EnsureCerts is idempotent: if the state dir already has all three files
// it returns their paths immediately without contacting the Panel.
package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
)

// CertPaths are the on-disk locations of a persisted Agent cert bundle.
type CertPaths struct {
	Cert string // agent.pem
	Key  string // agent-key.pem
	CA   string // ca.pem
}

func pathsIn(dir string) CertPaths {
	return CertPaths{
		Cert: filepath.Join(dir, "agent.pem"),
		Key:  filepath.Join(dir, "agent-key.pem"),
		CA:   filepath.Join(dir, "ca.pem"),
	}
}

// exists returns true iff every path in p is present + non-empty on disk.
func (p CertPaths) exists() bool {
	for _, f := range []string{p.Cert, p.Key, p.CA} {
		st, err := os.Stat(f)
		if err != nil || st.Size() == 0 {
			return false
		}
	}
	return true
}

// EnsureCerts guarantees a valid Agent mTLS bundle at ${stateDir}. If files
// already exist it returns their paths; otherwise it enrolls with panelURL
// and persists the returned bundle. The Panel is polled for up to `deadline`
// so a slightly-slow Panel process doesn't fail startup.
//
// panelURL is expected to be reachable on loopback (that's how the
// server-side loopback gate authenticates the enrollment). Setting it to
// anything else is a supported operator override but the request will fail.
func EnsureCerts(ctx context.Context, panelURL, stateDir string, extraHosts []string, deadline time.Duration, logger *slog.Logger) (CertPaths, error) {
	paths := pathsIn(stateDir)
	if paths.exists() {
		logger.Info("auto-enroll: reusing existing cert bundle", "dir", stateDir)
		return paths, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return CertPaths{}, fmt.Errorf("auto-enroll: mkdir state dir: %w", err)
	}

	base := strings.TrimRight(panelURL, "/")
	client := &http.Client{Timeout: 15 * time.Second}

	// The Panel process may still be coming up (docker-compose starts both
	// containers at roughly the same time). Poll /healthz until it answers
	// or the overall deadline expires.
	if err := waitForPanel(ctx, client, base, deadline, logger); err != nil {
		return CertPaths{}, err
	}

	token, err := fetchLocalToken(ctx, client, base)
	if err != nil {
		return CertPaths{}, fmt.Errorf("auto-enroll: fetch local token: %w", err)
	}
	logger.Info("auto-enroll: obtained bootstrap token from Panel")

	keyPEM, csrPEM, err := mtls.NewAgentKeyAndCSR(extraHosts)
	if err != nil {
		return CertPaths{}, fmt.Errorf("auto-enroll: generate key/CSR: %w", err)
	}
	certPEM, caPEM, err := exchangeCSR(ctx, client, base, token, csrPEM)
	if err != nil {
		return CertPaths{}, fmt.Errorf("auto-enroll: CSR exchange: %w", err)
	}

	if err := writeSecret(paths.Key, keyPEM); err != nil {
		return CertPaths{}, err
	}
	if err := writeSecret(paths.Cert, certPEM); err != nil {
		return CertPaths{}, err
	}
	if err := writeSecret(paths.CA, caPEM); err != nil {
		return CertPaths{}, err
	}
	logger.Info("auto-enroll: wrote cert bundle", "cert", paths.Cert, "ca", paths.CA)
	return paths, nil
}

// waitForPanel polls GET {base}/healthz until it returns 200 or the deadline
// expires. Bounded so a mis-configured KRAKEN_PANEL_URL fails cleanly.
func waitForPanel(ctx context.Context, client *http.Client, base string, deadline time.Duration, logger *slog.Logger) error {
	cctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	url := base + "/healthz"
	backoff := 500 * time.Millisecond
	for {
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-cctx.Done():
			return fmt.Errorf("auto-enroll: Panel at %s not ready within %s: %w", base, deadline, cctx.Err())
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
		logger.Info("auto-enroll: waiting for Panel", "url", url)
	}
}

func fetchLocalToken(ctx context.Context, client *http.Client, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/setup/local-enroll", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("panel %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if payload.Token == "" {
		return "", errors.New("panel returned an empty token")
	}
	return payload.Token, nil
}

func exchangeCSR(ctx context.Context, client *http.Client, base, token string, csrPEM []byte) (certPEM, caPEM []byte, err error) {
	body, _ := json.Marshal(map[string]string{"token": token, "csr": string(csrPEM)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/agents/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("panel %s: %s", resp.Status, strings.TrimSpace(string(rb)))
	}
	var er struct {
		Certificate string `json:"certificate"`
		CA          string `json:"ca"`
	}
	if err := json.Unmarshal(rb, &er); err != nil {
		return nil, nil, fmt.Errorf("decode: %w", err)
	}
	if er.Certificate == "" || er.CA == "" {
		return nil, nil, errors.New("panel returned empty certificate or ca")
	}
	return []byte(er.Certificate), []byte(er.CA), nil
}

// writeSecret writes cert/key material with 0600 perms.
func writeSecret(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
