package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// sftpAuthorizer resolves an SFTP login (username = server id) to the data-dir
// root the connection may access. Implemented by DockerRuntime.
type sftpAuthorizer interface {
	authorizeSFTPPassword(username, password string) (root string, ok bool)
	authorizeSFTPKey(username string, key ssh.PublicKey) (root string, ok bool)
}

// SFTPServer is the Agent's SSH/SFTP server. Every authenticated connection is
// chrooted to a single game server's data dir (per-server credentials), so it
// gives operators direct file access without the Panel proxying bytes.
type SFTPServer struct {
	auth   sftpAuthorizer
	signer ssh.Signer
	logger *slog.Logger
	lis    net.Listener
}

// StartSFTP starts the SFTP server in the background when the runtime supports
// per-server credentials (DockerRuntime). It returns nil (no server, no error)
// for runtimes that don't — e.g. the fake runtime in tests/dev.
func StartSFTP(rt Runtime, addr, hostKeyPath string, logger *slog.Logger) (*SFTPServer, error) {
	auth, ok := rt.(sftpAuthorizer)
	if !ok {
		return nil, nil
	}
	signer, err := loadOrCreateHostKey(hostKeyPath)
	if err != nil {
		return nil, err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sftp: listen %s: %w", addr, err)
	}
	// Record the bound port so NodeInfo can advertise it to the Panel (for the
	// connection details shown on the Files tab).
	if d, ok := rt.(*DockerRuntime); ok {
		if tcp, ok := lis.Addr().(*net.TCPAddr); ok {
			d.setSFTPPort(int32(tcp.Port))
		}
	}
	s := &SFTPServer{auth: auth, signer: signer, logger: logger, lis: lis}
	go s.serve()
	return s, nil
}

// Close stops accepting new SFTP connections.
func (s *SFTPServer) Close() error {
	if s.lis != nil {
		return s.lis.Close()
	}
	return nil
}

func (s *SFTPServer) sshConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			root, ok := s.auth.authorizeSFTPPassword(c.User(), string(pass))
			if !ok {
				return nil, errors.New("permission denied")
			}
			return &ssh.Permissions{Extensions: map[string]string{"root": root}}, nil
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			root, ok := s.auth.authorizeSFTPKey(c.User(), key)
			if !ok {
				return nil, errors.New("permission denied")
			}
			return &ssh.Permissions{Extensions: map[string]string{"root": root}}, nil
		},
	}
	cfg.AddHostKey(s.signer)
	return cfg
}

func (s *SFTPServer) serve() {
	cfg := s.sshConfig()
	for {
		nc, err := s.lis.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(nc, cfg)
	}
}

func (s *SFTPServer) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer func() { _ = sconn.Close() }()
	root := sconn.Permissions.Extensions["root"]
	if root == "" {
		return
	}
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
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
					srv := sftp.NewRequestServer(ch, rootedHandlers(root))
					_ = srv.Serve()
					_ = srv.Close()
					_ = ch.Close()
				}
			}
		}(requests)
	}
}

// ---- DockerRuntime SFTP authorization (username = server id) ----

// authorizeSFTPPassword validates an SFTP password login against the server's
// pushed credentials and returns the data-dir root to jail the connection to.
func (d *DockerRuntime) authorizeSFTPPassword(username, password string) (string, bool) {
	acc := d.sftpAccess(username)
	if acc == nil || !acc.GetEnabled() || acc.GetPasswordHash() == "" {
		return "", false
	}
	if bcrypt.CompareHashAndPassword([]byte(acc.GetPasswordHash()), []byte(password)) != nil {
		return "", false
	}
	return d.hostDir(username), true
}

// authorizeSFTPKey validates an SFTP public-key login against the server's
// authorized keys.
func (d *DockerRuntime) authorizeSFTPKey(username string, key ssh.PublicKey) (string, bool) {
	acc := d.sftpAccess(username)
	if acc == nil || !acc.GetEnabled() {
		return "", false
	}
	want := key.Marshal()
	for _, line := range acc.GetAuthorizedKeys() {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err == nil && bytes.Equal(pk.Marshal(), want) {
			return d.hostDir(username), true
		}
	}
	return "", false
}

func (d *DockerRuntime) sftpAccess(serverID string) *agentpb.SftpAccess {
	sp, ok := d.getSpec(serverID)
	if !ok {
		return nil
	}
	return sp.GetSftp()
}

// setSFTPPort records the port the SFTP server bound, so NodeInfo can report it.
func (d *DockerRuntime) setSFTPPort(p int32) {
	d.bmu.Lock()
	d.sftpPort = p
	d.bmu.Unlock()
}

// ---- rooted (chrooted) filesystem handlers ----

// rootFS confines every SFTP request to root: a client path like "/a/../b" is
// cleaned against "/" (so ".." can never climb above the jail) then joined to
// root. This is the same containment idea as the Agent's safePath.
type rootFS struct{ root string }

func rootedHandlers(root string) sftp.Handlers {
	fs := &rootFS{root: root}
	return sftp.Handlers{FileGet: fs, FilePut: fs, FileCmd: fs, FileList: fs}
}

func (f *rootFS) real(p string) string {
	clean := filepath.Clean("/" + strings.TrimPrefix(filepath.ToSlash(p), "/"))
	return filepath.Join(f.root, filepath.FromSlash(clean))
}

func (f *rootFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	return os.OpenFile(f.real(r.Filepath), os.O_RDONLY, 0)
}

func (f *rootFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	real := f.real(r.Filepath)
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(real, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
}

func (f *rootFS) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		return nil // accept chmod/times as a no-op rather than failing clients
	case "Rename", "PosixRename":
		return os.Rename(f.real(r.Filepath), f.real(r.Target))
	case "Rmdir", "Remove":
		return os.Remove(f.real(r.Filepath))
	case "Mkdir":
		return os.MkdirAll(f.real(r.Filepath), 0o755)
	default:
		// Symlink and anything else is refused — no escaping the jail via links.
		return sftp.ErrSSHFxOpUnsupported
	}
}

func (f *rootFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		entries, err := os.ReadDir(f.real(r.Filepath))
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			if fi, ierr := e.Info(); ierr == nil {
				infos = append(infos, fi)
			}
		}
		return listerat(infos), nil
	case "Stat":
		fi, err := os.Stat(f.real(r.Filepath))
		if err != nil {
			return nil, err
		}
		return listerat{fi}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

// listerat adapts a slice of FileInfo to sftp.ListerAt (paginated directory read).
type listerat []os.FileInfo

func (l listerat) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

// loadOrCreateHostKey loads a persisted SSH host key, generating + saving an
// ed25519 one on first use so the server's identity is stable across restarts.
func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return ssh.ParsePrivateKey(b)
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sftp: generate host key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("sftp: marshal host key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			_ = os.WriteFile(path, pemBytes, 0o600) // best-effort persist
		}
	}
	return ssh.NewSignerFromKey(priv)
}
