package api

import (
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

// mirrorTarget names only the kind and host, never a credential, and reads as
// "off" whenever replication is disabled or the destination is half-configured.
func TestMirrorTarget(t *testing.T) {
	cases := []struct {
		name string
		cfg  store.NodeConfig
		want string
	}{
		{"off", store.NodeConfig{}, ""},
		{"sftp", store.NodeConfig{ReplicateToSftp: true, SftpHost: "nas.local", SftpUser: "kraken", SftpPassword: "hunter2"}, "sftp nas.local"},
		{"smb with share", store.NodeConfig{ReplicateToSmb: true, SmbHost: "nas.local", SmbShare: "backups"}, "smb nas.local/backups"},
		{"smb no share", store.NodeConfig{ReplicateToSmb: true, SmbHost: "nas.local"}, "smb nas.local"},
		{"toggle on but no host", store.NodeConfig{ReplicateToSftp: true}, ""},
		{"sftp wins when both set", store.NodeConfig{ReplicateToSftp: true, SftpHost: "sftp.host", ReplicateToSmb: true, SmbHost: "smb.host"}, "sftp sftp.host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mirrorTarget(&c.cfg)
			if got != c.want {
				t.Errorf("mirrorTarget = %q, want %q", got, c.want)
			}
			// A credential must never appear in the display string.
			if c.cfg.SftpPassword != "" && strings.Contains(got, c.cfg.SftpPassword) {
				t.Errorf("mirrorTarget %q leaked a credential", got)
			}
		})
	}
}
