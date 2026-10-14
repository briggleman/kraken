package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

// A handler that names its own action does it through recordAuditDetail, which
// claims the middleware's row: one request, one row, and the actor is the user.
// Passing the action to recordAudit instead made it the ACTOR, beside the
// middleware's own row — every forward, DNS publish, cordon, node-config save
// and SFTP change was logged twice, once "by" a string like "node-cordon:<id>".
func TestAuditHandlerActionIsOneRowByTheUser(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	id := registerNode(t, h, token, startFakeAgent(t, "node-audit"))

	if rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+id+"/cordon", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("cordon: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodGet, "/api/v1/audit", token, nil)
	var out struct {
		Entries []store.AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	var rows []store.AuditEntry
	for _, e := range out.Entries {
		if strings.Contains(e.Action, "cordon") || strings.Contains(e.Actor, "cordon") {
			rows = append(rows, e)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("cordon wrote %d audit rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Actor != "admin" || rows[0].Action != "node-cordon:"+id {
		t.Fatalf("row = actor %q action %q; want the user and the handler's action", rows[0].Actor, rows[0].Action)
	}
}
