package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// startTestSFTPServer stands up an in-process SSH server with an SFTP subsystem
// rooted (via the server working directory) at root, accepting user "kraken" /
// password "hunter2". It returns the listener address.
func startTestSFTPServer(t *testing.T, root string) string {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "kraken" && string(pass) == "hunter2" {
				return nil, nil
			}
			return nil, errors.New("auth failed")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			nc, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go serveTestSFTPConn(nc, cfg, root)
		}
	}()
	return ln.Addr().String()
}

func serveTestSFTPConn(nc net.Conn, cfg *ssh.ServerConfig, root string) {
	sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, aerr := newCh.Accept()
		if aerr != nil {
			continue
		}
		go func(in <-chan *ssh.Request) {
			for req := range in {
				// The subsystem name is a length-prefixed string; "sftp" follows 4 bytes.
				ok := req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp"
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
				if ok {
					srv, serr := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(root))
					if serr == nil {
						_ = srv.Serve()
					}
					_ = ch.Close()
				}
			}
		}(requests)
	}
}

func newTestSFTPTarget(addr string) *sftpBackupTarget {
	return &sftpBackupTarget{cfg: sftpConfig{Host: addr, User: "kraken", Password: "hunter2", BasePath: "backups"}}
}

func TestSFTPBackupTargetRoundTrip(t *testing.T) {
	root := t.TempDir()
	addr := startTestSFTPServer(t, root)
	tgt := newTestSFTPTarget(addr)
	ctx := context.Background()

	if err := tgt.verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}

	payload := []byte("fake-archive-bytes")
	id := "1700000000__nightly"
	if err := tgt.Put(ctx, "srv1", id, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	list, err := tgt.List(ctx, "srv1")
	if err != nil || len(list) != 1 || list[0].Id != id || list[0].Name != "nightly" {
		t.Fatalf("List: %+v err=%v", list, err)
	}

	rc, err := tgt.Open(ctx, "srv1", id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: got %q", got)
	}

	// A base path with no archives yet lists empty, not an error (the missing-dir
	// branch). Archives live directly in BasePath now (no per-server subdir), so
	// server scoping comes from the configured path, e.g. {{SLUG}}.
	fresh := &sftpBackupTarget{cfg: sftpConfig{Host: addr, User: "kraken", Password: "hunter2", BasePath: "backups/empty"}}
	if l, lerr := fresh.List(ctx, "srv1"); lerr != nil || len(l) != 0 {
		t.Fatalf("List(empty base): %+v err=%v", l, lerr)
	}

	if err := tgt.Delete(ctx, "srv1", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if l, _ := tgt.List(ctx, "srv1"); len(l) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(l))
	}
}

func TestSFTPBackupTargetJail(t *testing.T) {
	root := t.TempDir()
	tgt := newTestSFTPTarget(startTestSFTPServer(t, root))
	ctx := context.Background()

	// A traversal id must be reduced to its base name so it lands inside the jail.
	if err := tgt.Put(ctx, "srv1", "../../../etc/evil", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	jailed := filepath.Join(root, "backups", "evil.tar.gz")
	if _, err := os.Stat(jailed); err != nil {
		t.Fatalf("expected jailed file at %s: %v", jailed, err)
	}
	escaped := filepath.Join(filepath.Dir(root), "etc", "evil.tar.gz")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal escaped the jail to %s", escaped)
	}
}
