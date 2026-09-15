package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

// auditEntries reads the audit log as the admin.
func auditEntries(t *testing.T, e *downloadEnv) []store.AuditEntry {
	t.Helper()
	rec := do(t, e.h, http.MethodGet, "/api/v1/audit", e.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: got %d", rec.Code)
	}
	var out struct {
		Entries []store.AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	return out.Entries
}

// A download that fails on its very first chunk must arrive as an error, not as
// a file. Headers set before the first Recv cannot be taken back, and a real
// anchor navigation would then save the JSON error to disk under the name the
// operator asked for — with nothing on screen to say so, because no JS is
// watching the outcome of a navigation.
func TestDownloadFailureDoesNotArriveAsAFile(t *testing.T) {
	e := newDownloadEnv(t)
	var body struct {
		Error string `json:"error"`
	}

	// Raw: a path the Agent has nothing for, so the first Recv is the failure.
	raw := do(t, e.h, http.MethodGet,
		"/api/v1/servers/"+e.server+"/files/raw?path=/data/no-such-file", e.token, nil)
	if raw.Code != http.StatusBadGateway {
		t.Fatalf("raw download of a missing file: got %d, want 502", raw.Code)
	}
	if cd := raw.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("a failed download carried Content-Disposition %q — the browser would save the error as the file", cd)
	}
	if ct := raw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", ct)
	}
	if err := json.Unmarshal(raw.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("failed download body = %q, want a JSON error envelope", raw.Body.String())
	}

	// Zip, through the token route — the one with no JS watching at all. The
	// Agent is made unreachable rather than the folder made missing: the fake
	// runtime's zip writer is happy to archive a path that is not there, so an
	// unreachable node is the honest way to fail the first chunk.
	tok, _ := e.mint(t, e.token, e.server, map[string]any{"paths": []string{fakeSavePth}})
	ctx := context.Background()
	node, err := e.st.GetNode(ctx, e.nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	node.Address = "127.0.0.1:1"
	if err := e.st.UpdateNode(ctx, node); err != nil {
		t.Fatalf("update node: %v", err)
	}
	z := do(t, e.h, http.MethodGet, tok.URL, "", nil)
	if z.Code != http.StatusBadGateway {
		t.Fatalf("zip of a missing folder: got %d, want 502", z.Code)
	}
	if cd := z.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("a failed zip carried Content-Disposition %q", cd)
	}
	if err := json.Unmarshal(z.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("failed zip body = %q, want a JSON error envelope", z.Body.String())
	}
}

// The redemption's audit row records what actually happened, not that a token
// was spent: a redemption the Agent could not serve must not read as a
// completed download.
func TestDownloadTokenAuditsTheOutcomeNotTheIntent(t *testing.T) {
	e := newDownloadEnv(t)
	tok, _ := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})

	// Point the node at a port nothing is listening on: the Agent is now
	// unreachable, and the stream fails on its first chunk.
	ctx := context.Background()
	node, err := e.st.GetNode(ctx, e.nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	node.Address = "127.0.0.1:1"
	if err := e.st.UpdateNode(ctx, node); err != nil {
		t.Fatalf("update node: %v", err)
	}

	if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusBadGateway {
		t.Fatalf("redeem against an unreachable agent: got %d, want 502", rec.Code)
	}
	for _, ent := range auditEntries(t, e) {
		if strings.Contains(ent.Action, "download token redeemed") {
			if ent.Status != http.StatusBadGateway {
				t.Fatalf("the redemption audited as %d, want 502 — the row claims a download that never happened", ent.Status)
			}
			return
		}
	}
	t.Fatal("no audit entry for the redemption")
}

// Content-Disposition is built from a name the operator (or anyone who can
// write to the tree) chose, so it is sanitised on both halves: the ASCII
// filename= carries nothing a client can mis-render, and the RFC 5987
// filename*= carries the real name percent-encoded.
func TestDownloadFilenameIsSanitised(t *testing.T) {
	e := newDownloadEnv(t)

	write := func(p, content string) {
		rec := do(t, e.h, http.MethodPost, "/api/v1/servers/"+e.server+"/files/write", e.token,
			map[string]string{"path": p, "content": content})
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed %q: got %d, body %s", p, rec.Code, rec.Body.String())
		}
	}
	// A bidi override (U+202E) in the name: rendered naively, the tail
	// "gpj.exe" after it reads as "exe.jpg" in a save prompt. Written as an
	// escape so this source file does not render backwards either.
	bidi := "/data/holiday\u202Egpj.exe"
	write(bidi, "x")
	// A non-ASCII name that is perfectly legitimate and must survive.
	utf8Name := "/data/welt-grüße.cfg"
	write(utf8Name, "y")

	get := func(p string) string {
		rec := do(t, e.h, http.MethodGet,
			"/api/v1/servers/"+e.server+"/files/raw?path="+p, e.token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("download %q: got %d, body %s", p, rec.Code, rec.Body.String())
		}
		return rec.Header().Get("Content-Disposition")
	}

	cd := get(bidi)
	if strings.ContainsRune(cd, '\u202E') {
		t.Fatalf("Content-Disposition %q still carries the bidi override", cd)
	}
	if !strings.Contains(cd, `filename="holidaygpj.exe"`) {
		t.Fatalf("Content-Disposition = %q, want the override dropped from the ASCII name", cd)
	}

	cd = get(utf8Name)
	// The ASCII half flattens the umlauts; the RFC 5987 half carries them.
	if !strings.Contains(cd, `filename="welt-gr__e.cfg"`) {
		t.Fatalf("Content-Disposition = %q, want an ASCII-safe filename=", cd)
	}
	if !strings.Contains(cd, "filename*=UTF-8''welt-gr%C3%BC%C3%9Fe.cfg") {
		t.Fatalf("Content-Disposition = %q, want the real name in an RFC 5987 filename*", cd)
	}
}

// A server handed to another owner inside the token's 60 seconds must fail the
// redemption, not merely be caught downstream.
func TestDownloadTokenRejectedAfterServerIsReOwned(t *testing.T) {
	e := newDownloadEnv(t)
	ctx := context.Background()
	sess := seedDownloadOperator(t, e, "op3")

	tok, code := e.mint(t, sess, e.server, map[string]any{"path": fakeCfgPath})
	if code != http.StatusCreated {
		t.Fatalf("operator mint: got %d, want 201", code)
	}
	sv, err := e.st.GetServer(ctx, e.server)
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	sv.OwnerID = "someone-else"
	if err := e.st.UpdateServer(ctx, sv); err != nil {
		t.Fatalf("update server owner: %v", err)
	}
	if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("redeem after the server was re-owned: got %d, want 401", rec.Code)
	}
}
