package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePanel simulates the two endpoints EnsureCerts talks to. `healthAfter`
// health probes return 500; probes >= that count return 200. Each successful
// enroll issues a fresh self-signed cert so the caller can verify the bundle.
type fakePanel struct {
	server         *httptest.Server
	healthAfter    int32
	healthProbes   atomic.Int32
	tokenIssued    atomic.Int32
	enrollRequests atomic.Int32
	fixedToken     string
	caCert         []byte
	caKey          *ecdsa.PrivateKey
}

func newFakePanel(t *testing.T, healthAfter int, tok string) *fakePanel {
	t.Helper()
	fp := &fakePanel{healthAfter: int32(healthAfter), fixedToken: tok}
	// Build a throw-away CA used to sign the enrollment response certs.
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kraken-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatalf("sign ca: %v", err)
	}
	fp.caCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	fp.caKey = k

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		n := fp.healthProbes.Add(1)
		if n <= fp.healthAfter {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/setup/local-enroll", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fp.tokenIssued.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": fp.fixedToken})
	})
	mux.HandleFunc("/api/v1/agents/enroll", func(w http.ResponseWriter, r *http.Request) {
		fp.enrollRequests.Add(1)
		var body struct{ Token, CSR string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Token != fp.fixedToken || body.CSR == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		leafPEM := fp.signCSR(t, body.CSR)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificate": string(leafPEM),
			"ca":          string(fp.caCert),
		})
	})
	fp.server = httptest.NewServer(mux)
	t.Cleanup(fp.server.Close)
	return fp
}

func (fp *fakePanel) signCSR(t *testing.T, csrPEM string) []byte {
	t.Helper()
	blk, _ := pem.Decode([]byte(csrPEM))
	if blk == nil {
		t.Fatal("bad csr pem")
	}
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		t.Fatalf("parse csr: %v", err)
	}
	caBlk, _ := pem.Decode(fp.caCert)
	caCert, _ := x509.ParseCertificate(caBlk.Bytes)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      csr.Subject,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"kraken-agent"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, csr.PublicKey, fp.caKey)
	if err != nil {
		t.Fatalf("sign leaf: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
}

// TestEnsureCerts_HappyPath — fresh state dir + healthy Panel produces
// three cert files on disk with 0600 perms and non-empty PEM contents.
func TestEnsureCerts_HappyPath(t *testing.T) {
	fp := newFakePanel(t, 0, "bootstrap-tok")
	dir := t.TempDir()
	paths, err := EnsureCerts(context.Background(), fp.server.URL, dir, nil, 5*time.Second, testLogger())
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	for _, p := range []string{paths.Cert, paths.Key, paths.CA} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", p, err)
		}
		if st.Size() == 0 {
			t.Fatalf("%s is empty", p)
		}
	}
	// The private key must be 0600 on POSIX; on Windows perms are relaxed
	// so only check when Unix perms are meaningful (Size check above is
	// sufficient on Windows).
	if info, _ := os.Stat(paths.Key); info != nil && info.Mode()&0o077 != 0 && !isWindows() {
		t.Errorf("key file mode %v allows group/other read", info.Mode())
	}
	if fp.enrollRequests.Load() != 1 {
		t.Errorf("expected exactly 1 enroll call, got %d", fp.enrollRequests.Load())
	}
}

// TestEnsureCerts_Idempotent — rerunning with a populated state dir must
// not re-contact the Panel. Guards against a slow restart re-enrolling and
// creating orphan certs on every crash.
func TestEnsureCerts_Idempotent(t *testing.T) {
	fp := newFakePanel(t, 0, "bootstrap-tok")
	dir := t.TempDir()
	// Seed the state dir with fake bundle contents.
	for _, name := range []string{"agent.pem", "agent-key.pem", "ca.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("existing-material"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := EnsureCerts(context.Background(), fp.server.URL, dir, nil, 5*time.Second, testLogger()); err != nil {
		t.Fatalf("enroll (idempotent): %v", err)
	}
	if got := fp.tokenIssued.Load() + fp.enrollRequests.Load() + fp.healthProbes.Load(); got != 0 {
		t.Errorf("expected zero HTTP calls when bundle exists, got %d", got)
	}
}

// TestEnsureCerts_WaitsForPanel — /healthz initially returns 500, then flips
// to 200. Enrollment must succeed after retry.
func TestEnsureCerts_WaitsForPanel(t *testing.T) {
	fp := newFakePanel(t, 2, "bootstrap-tok") // first 2 probes fail
	dir := t.TempDir()
	if _, err := EnsureCerts(context.Background(), fp.server.URL, dir, nil, 10*time.Second, testLogger()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if fp.healthProbes.Load() < 3 {
		t.Errorf("expected at least 3 health probes, got %d", fp.healthProbes.Load())
	}
	if fp.enrollRequests.Load() != 1 {
		t.Errorf("expected 1 enroll call, got %d", fp.enrollRequests.Load())
	}
}

// TestEnsureCerts_UnreachablePanel — an unreachable base URL must fail
// within the deadline rather than hanging.
func TestEnsureCerts_UnreachablePanel(t *testing.T) {
	dir := t.TempDir()
	// Point at a port that is (almost certainly) closed. The 127.0.0.1
	// address makes the failure prompt.
	_, err := EnsureCerts(context.Background(), "http://127.0.0.1:1", dir, nil, 2*time.Second, testLogger())
	if err == nil {
		t.Fatal("expected error from unreachable Panel")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected 'not ready' in error, got: %v", err)
	}
}

func isWindows() bool { return os.PathSeparator == '\\' }
