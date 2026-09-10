package agent

import (
	"fmt"
	"os"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// There is no in-process SMB server to test against (no Go server library), so
// these cover the parts that run without one: config → target construction,
// address/path normalization and error classification. A live round trip is
// drilled against the fleet NAS.

func TestSMBHostPortDefaultsTo445(t *testing.T) {
	for in, want := range map[string]string{
		"nas.lan":         "nas.lan:445",
		"10.0.0.5":        "10.0.0.5:445",
		"nas.lan:4450":    "nas.lan:4450",
		"  nas.lan  ":     "nas.lan:445",
		"fe80::1":         "[fe80::1]:445",
		"[fe80::1]":       "[fe80::1]:445",
		"[fe80::1]:445":   "[fe80::1]:445",
		"10.0.0.5:445":    "10.0.0.5:445",
		"nas.example.com": "nas.example.com:445",
	} {
		got, err := smbHostPort(in)
		if err != nil {
			t.Errorf("smbHostPort(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("smbHostPort(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := smbHostPort("   "); err == nil {
		t.Error("an empty host must be an error — dialing \":445\" would hit the local machine")
	}
}

func TestSMBShareNameStripsServerAndSeparators(t *testing.T) {
	for in, want := range map[string]string{
		"games":            "games",
		"/games":           "games",
		`\games`:           "games",
		`\\nas\games`:      "games",
		"//nas/games/":     "games",
		"  games  ":        "games",
		`\\nas.lan\Backup`: "Backup",
		"":                 "",
	} {
		if got := smbShareName(in); got != want {
			t.Errorf("smbShareName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The client rejects a leading separator outright (SMB paths are share-relative),
// so an operator typing "/kraken/backups" must still resolve.
func TestSMBRelPathIsShareRelative(t *testing.T) {
	for in, want := range map[string]string{
		"kraken/backups":    "kraken/backups",
		"/kraken/backups":   "kraken/backups",
		`\kraken\backups\`:  "kraken/backups",
		"kraken//backups":   "kraken/backups",
		"kraken/./backups":  "kraken/backups",
		"  /kraken/  ":      "kraken",
		"":                  "",
		"/":                 "",
		".":                 "",
		"kraken/palworld/x": "kraken/palworld/x",
	} {
		if got := smbRelPath(in); got != want {
			t.Errorf("smbRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Archives live flat inside the expanded base path — the same layout as the
// sftp target — and a traversal id is reduced to its base name.
func TestSMBRemotePathLayout(t *testing.T) {
	tgt := &smbBackupTarget{cfg: smbConfig{BasePath: "/kraken/palworld/"}}
	if got := tgt.remoteDir(); got != "kraken/palworld" {
		t.Errorf("remoteDir = %q, want kraken/palworld", got)
	}
	if got := tgt.remotePath("srv1", "1700000000__nightly"); got != "kraken/palworld/1700000000__nightly.tar.gz" {
		t.Errorf("remotePath = %q", got)
	}
	if got := tgt.remotePath("srv1", "../../etc/evil"); got != "kraken/palworld/evil.tar.gz" {
		t.Errorf("traversal id not jailed: %q", got)
	}
	root := &smbBackupTarget{cfg: smbConfig{}}
	if got := root.remotePath("srv1", "id"); got != "id.tar.gz" {
		t.Errorf("empty base path must resolve to the share root, got %q", got)
	}
}

// buildTarget must select the SMB target and receive a {{SLUG}}-expanded base
// path — expandCfgPaths is the only place that expansion happens for per-server
// operations, so a field missing there is silently lost.
func TestBuildTargetSMBFromNodeConfig(t *testing.T) {
	d := &DockerRuntime{backupDir: "/var/backups"}
	cfg := &agentpb.NodeConfig{
		BackupTarget: "smb",
		SmbHost:      "nas.lan",
		SmbShare:     "games",
		SmbUser:      "kraken",
		SmbPassword:  "hunter2",
		SmbDomain:    "WORKGROUP",
		SmbBasePath:  "kraken/{{SLUG}}/backup",
	}
	tgt := d.buildTarget(expandCfgPaths(cfg, "palworld"))
	st, ok := tgt.(*smbBackupTarget)
	if !ok {
		t.Fatalf("buildTarget(smb) = %T, want *smbBackupTarget", tgt)
	}
	if st.Kind() != "smb" {
		t.Errorf("Kind = %q, want smb", st.Kind())
	}
	want := smbConfig{Host: "nas.lan", Share: "games", User: "kraken", Password: "hunter2", Domain: "WORKGROUP", BasePath: "kraken/palworld/backup"}
	if st.cfg != want {
		t.Errorf("cfg = %+v, want %+v", st.cfg, want)
	}
}

// Replication picks exactly one mirror; sftp wins if a hand-edited config sets
// both (the Panel rejects that combination).
func TestBuildReplicateTargetSelectsOneMirror(t *testing.T) {
	base := func() *agentpb.NodeConfig {
		return &agentpb.NodeConfig{
			SftpHost: "sftp.lan", SftpUser: "kraken", SftpPassword: "x",
			SmbHost: "nas.lan", SmbShare: "games", SmbUser: "kraken", SmbPassword: "x",
		}
	}
	if got := buildReplicateTarget(base()); got != nil {
		t.Errorf("no replication flags: got %T, want nil", got)
	}
	smbCfg := base()
	smbCfg.ReplicateToSmb = true
	if got := buildReplicateTarget(smbCfg); got == nil || got.Kind() != "smb" {
		t.Errorf("replicate_to_smb: got %v", got)
	}
	sftpCfg := base()
	sftpCfg.ReplicateToSftp = true
	if got := buildReplicateTarget(sftpCfg); got == nil || got.Kind() != "sftp" {
		t.Errorf("replicate_to_sftp: got %v", got)
	}
	both := base()
	both.ReplicateToSftp = true
	both.ReplicateToSmb = true
	if got := buildReplicateTarget(both); got == nil || got.Kind() != "sftp" {
		t.Errorf("both flags: got %v, want the sftp mirror", got)
	}
}

// The client surfaces STATUS_OBJECT_{NAME,PATH}_NOT_FOUND as os.ErrNotExist
// inside an *os.PathError, which is what "no backups yet" and "already deleted"
// depend on.
func TestIsSMBNotExist(t *testing.T) {
	if !isSMBNotExist(&os.PathError{Op: "open", Path: "x", Err: os.ErrNotExist}) {
		t.Error("wrapped os.ErrNotExist must classify as not-exist")
	}
	if !isSMBNotExist(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: os.ErrNotExist}) {
		t.Error("a rename's *os.LinkError must classify as not-exist")
	}
	if isSMBNotExist(nil) {
		t.Error("nil is not a not-exist error")
	}
	if isSMBNotExist(fmt.Errorf("response error: STATUS_ACCESS_DENIED")) {
		t.Error("an unrelated error must not classify as not-exist")
	}
}
