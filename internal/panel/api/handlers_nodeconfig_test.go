package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// nodeCfgView mirrors the fields of nodeConfigView these tests assert on.
// Secrets have no field here by design — the view only reports whether one is
// stored.
type nodeCfgView struct {
	BackupTarget          string `json:"backup_target"`
	SmbHost               string `json:"smb_host"`
	SmbShare              string `json:"smb_share"`
	SmbUser               string `json:"smb_user"`
	SmbDomain             string `json:"smb_domain"`
	SmbBasePath           string `json:"smb_base_path"`
	SmbPasswordConfigured bool   `json:"smb_password_configured"`
	ReplicateToSmb        bool   `json:"replicate_to_smb"`
	ReplicateToSftp       bool   `json:"replicate_to_sftp"`
}

// The SMB target round-trips through the config endpoints: every non-secret
// field comes back, the password is stored but never echoed, and the target
// name is accepted.
func TestNodeConfigSMBRoundTrip(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	id := registerNode(t, h, token, startFakeAgent(t, "node-smb"))

	rec := do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token, map[string]any{
		"backup_target": "smb",
		"smb_host":      "nas.lan",
		"smb_share":     "games",
		"smb_user":      "kraken",
		"smb_password":  "hunter2",
		"smb_domain":    "WORKGROUP",
		"smb_base_path": "kraken/{{SLUG}}/backup",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save smb config: status %d, body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "hunter2") {
		t.Fatalf("the smb password was echoed back: %s", body)
	}
	var v nodeCfgView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.BackupTarget != "smb" || v.SmbHost != "nas.lan" || v.SmbShare != "games" ||
		v.SmbUser != "kraken" || v.SmbDomain != "WORKGROUP" || v.SmbBasePath != "kraken/{{SLUG}}/backup" {
		t.Fatalf("smb fields not persisted: %+v", v)
	}
	if !v.SmbPasswordConfigured {
		t.Error("smb_password_configured should be true after storing a password")
	}

	// And it survives a reload, with the password still hidden.
	rec = do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/config", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get config: status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("the smb password leaked on read: %s", rec.Body.String())
	}
	v = nodeCfgView{}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.SmbHost != "nas.lan" || !v.SmbPasswordConfigured {
		t.Fatalf("config did not persist: %+v", v)
	}

	// An omitted field leaves its stored value alone — that is what lets the UI
	// send only what changed and never resend the password.
	rec = do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token, map[string]any{"smb_share": "backups"})
	if rec.Code != http.StatusOK {
		t.Fatalf("partial update: status %d, body %s", rec.Code, rec.Body.String())
	}
	v = nodeCfgView{}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.SmbShare != "backups" || !v.SmbPasswordConfigured || v.SmbHost != "nas.lan" {
		t.Fatalf("partial update disturbed other fields: %+v", v)
	}
}

// An unknown {{TOKEN}} in smb_base_path must fail at save time — the Agent only
// expands {{SLUG}}, so a typo would otherwise become a literal directory name.
func TestNodeConfigSMBRejectsUnknownPathToken(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	id := registerNode(t, h, token, startFakeAgent(t, "node-smb-token"))

	rec := do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token, map[string]any{
		"smb_base_path": "kraken/{{SLG}}/backup",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown token, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// One mirror destination per node: the Agent can only replicate to one remote,
// so both flags together is a refusal rather than an arbitrary pick.
func TestNodeConfigRejectsBothReplicationMirrors(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	id := registerNode(t, h, token, startFakeAgent(t, "node-two-mirrors"))

	rec := do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token, map[string]any{
		"replicate_to_sftp": true,
		"replicate_to_smb":  true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for both mirrors, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Enabling the second one on top of a stored first is the same refusal.
	if rec := do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token,
		map[string]any{"replicate_to_sftp": true}); rec.Code != http.StatusOK {
		t.Fatalf("enable sftp mirror: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token, map[string]any{"replicate_to_smb": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 adding a second mirror, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestNodeConfigRejectsUnknownBackupTarget(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	id := registerNode(t, h, token, startFakeAgent(t, "node-bad-target"))

	rec := do(t, h, http.MethodPut, "/api/v1/nodes/"+id+"/config", token, map[string]any{"backup_target": "ftp"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown target, got %d (%s)", rec.Code, rec.Body.String())
	}
}
