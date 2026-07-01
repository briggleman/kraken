package agent

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testSFTPAuth is a fixed authorizer: any login with the right password is
// jailed to root; keys are rejected.
type testSFTPAuth struct {
	root string
	pass string
}

func (a *testSFTPAuth) authorizeSFTPPassword(_ /*user*/, password string) (string, bool) {
	return a.root, password == a.pass
}
func (a *testSFTPAuth) authorizeSFTPKey(string, ssh.PublicKey) (string, bool) { return "", false }

// startTestSFTP spins up the SFTP server on an ephemeral port jailed to root.
func startTestSFTP(t *testing.T, root string) string {
	t.Helper()
	signer, err := loadOrCreateHostKey("")
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &SFTPServer{auth: &testSFTPAuth{root: root, pass: "hunter2"}, signer: signer, logger: slog.Default(), lis: lis}
	go s.serve()
	t.Cleanup(func() { _ = s.Close() })
	return lis.Addr().String()
}

func dialSFTP(t *testing.T, addr, password string) (*sftp.Client, func()) {
	t.Helper()
	cc := &ssh.ClientConfig{
		User:            "srv",
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- test
	}
	conn, err := ssh.Dial("tcp", addr, cc)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("sftp client: %v", err)
	}
	return client, func() { _ = client.Close(); _ = conn.Close() }
}

func TestSFTPServerRoundTripAndJail(t *testing.T) {
	root := t.TempDir()
	addr := startTestSFTP(t, root)

	client, closeFn := dialSFTP(t, addr, "hunter2")
	defer closeFn()

	// Write a file, read it back.
	f, err := client.Create("/world/level.dat")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write([]byte("savedata")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	if b, _ := os.ReadFile(filepath.Join(root, "world", "level.dat")); string(b) != "savedata" {
		t.Fatalf("file not written into jail: %q", b)
	}

	rf, err := client.Open("/world/level.dat")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, _ := io.ReadAll(rf)
	_ = rf.Close()
	if string(got) != "savedata" {
		t.Fatalf("read back = %q", got)
	}

	// Directory listing.
	entries, err := client.ReadDir("/world")
	if err != nil || len(entries) != 1 || entries[0].Name() != "level.dat" {
		t.Fatalf("readdir = %+v err=%v", entries, err)
	}

	// Jail escape: a traversal path is confined under root, never above it.
	esc, err := client.Create("/../../escape.txt")
	if err != nil {
		t.Fatalf("create traversal: %v", err)
	}
	_, _ = esc.Write([]byte("x"))
	_ = esc.Close()
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("traversal escaped the jail")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err != nil {
		t.Fatalf("traversal file should be clamped into root: %v", err)
	}
}

func TestSFTPServerRejectsBadPassword(t *testing.T) {
	addr := startTestSFTP(t, t.TempDir())
	cc := &ssh.ClientConfig{
		User:            "srv",
		Auth:            []ssh.AuthMethod{ssh.Password("wrong")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- test
	}
	if conn, err := ssh.Dial("tcp", addr, cc); err == nil {
		_ = conn.Close()
		t.Fatal("expected auth failure with wrong password")
	}
}
