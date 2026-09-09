package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarRoundTrip gunzips and fully reads an archive, returning name→body. It
// fails the test on any decode error — a poisoned stream is the failure mode
// zero-filling exists to prevent, so every archive test must round-trip.
func tarRoundTrip(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read %s: %v", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			t.Fatalf("entry %s: body %d bytes, header says %d", hdr.Name, len(body), hdr.Size)
		}
		out[hdr.Name] = body
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return out
}

// oneEntry writes a single tar entry of the given header size with r as the
// body via writeBody, closing the writers, and round-trips the result.
func oneEntry(t *testing.T, r io.Reader, size int64) (body []byte, grew, truncated bool) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "f", Mode: 0o644, Size: size}); err != nil {
		t.Fatalf("header: %v", err)
	}
	grew, truncated, err := writeBody(tw, r, size)
	if err != nil {
		t.Fatalf("writeBody: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return tarRoundTrip(t, buf.Bytes())["f"], grew, truncated
}

func TestWriteBodyExact(t *testing.T) {
	body, grew, truncated := oneEntry(t, strings.NewReader("0123456789"), 10)
	if grew || truncated {
		t.Fatalf("grew=%v truncated=%v, want false/false", grew, truncated)
	}
	if string(body) != "0123456789" {
		t.Fatalf("body = %q", body)
	}
}

func TestWriteBodyGrew(t *testing.T) {
	// Reader holds more than the committed header size — the entry captures the
	// prefix and the archive stays valid.
	body, grew, truncated := oneEntry(t, strings.NewReader("0123456789extra"), 10)
	if !grew || truncated {
		t.Fatalf("grew=%v truncated=%v, want true/false", grew, truncated)
	}
	if string(body) != "0123456789" {
		t.Fatalf("body = %q", body)
	}
}

func TestWriteBodyShrank(t *testing.T) {
	// Reader holds less than the committed header size — the tail is
	// zero-filled; a short entry would fail tar's Close.
	body, grew, truncated := oneEntry(t, strings.NewReader("0123"), 10)
	if grew || !truncated {
		t.Fatalf("grew=%v truncated=%v, want false/true", grew, truncated)
	}
	want := append([]byte("0123"), make([]byte, 6)...)
	if !bytes.Equal(body, want) {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestWriteBodyShrankHuge(t *testing.T) {
	// Padding larger than one zero block exercises the writeZeros loop.
	body, _, truncated := oneEntry(t, strings.NewReader("x"), 100_000)
	if !truncated {
		t.Fatal("want truncated")
	}
	if len(body) != 100_000 || body[0] != 'x' || body[99_999] != 0 {
		t.Fatalf("bad padded body: len=%d", len(body))
	}
}

func TestArchiveTreeRoundTrip(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		fp := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("server.cfg", "cfg")
	mustWrite("saves/world.db", "world-bytes")
	mustWrite("logs/latest.log", "log line")

	var buf bytes.Buffer
	st, err := archiveTree(root, &buf)
	if err != nil {
		t.Fatalf("archiveTree: %v", err)
	}
	if st.files != 3 {
		t.Fatalf("files = %d, want 3", st.files)
	}
	if !st.clean() {
		t.Fatalf("stats not clean: %+v (summary %q)", st, st.summary())
	}
	got := tarRoundTrip(t, buf.Bytes())
	for rel, want := range map[string]string{"server.cfg": "cfg", "saves/world.db": "world-bytes", "logs/latest.log": "log line"} {
		if string(got[rel]) != want {
			t.Errorf("%s = %q, want %q", rel, got[rel], want)
		}
	}
	for _, dir := range []string{"saves/", "logs/"} {
		if _, ok := got[dir]; !ok {
			t.Errorf("missing dir entry %s", dir)
		}
	}
}

func TestArchiveTreeSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err) // Windows without the privilege
	}

	var buf bytes.Buffer
	st, err := archiveTree(root, &buf)
	if err != nil {
		t.Fatalf("archiveTree: %v", err)
	}
	got := tarRoundTrip(t, buf.Bytes())
	if _, ok := got["link.txt"]; ok {
		t.Error("symlink was archived; want skipped")
	}
	if st.symlinks != 1 {
		t.Errorf("symlinks = %d, want 1", st.symlinks)
	}
	if st.clean() || st.summary() == "" {
		t.Errorf("degradation not reported: %+v", st)
	}
	if string(got["real.txt"]) != "real" {
		t.Errorf("real.txt = %q", got["real.txt"])
	}
}

func TestArchiveTreeLargeFileStreams(t *testing.T) {
	// Force the streaming path by dropping the small-file threshold.
	old := smallFileMax
	smallFileMax = 4
	defer func() { smallFileMax = old }()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st, err := archiveTree(root, &buf)
	if err != nil {
		t.Fatalf("archiveTree: %v", err)
	}
	if st.files != 1 || !st.clean() {
		t.Fatalf("stats: %+v", st)
	}
	if got := tarRoundTrip(t, buf.Bytes())["big.bin"]; string(got) != "0123456789" {
		t.Fatalf("big.bin = %q", got)
	}
}

func TestArchiveDataDirEmptyFails(t *testing.T) {
	var buf bytes.Buffer
	st, err := archiveTree(t.TempDir(), &buf)
	if err != nil {
		t.Fatalf("archiveTree on empty dir: %v", err)
	}
	if st.files != 0 {
		t.Fatalf("files = %d, want 0", st.files)
	}
	// The zero-files → error policy lives in archiveDataDir, which needs a
	// runtime; the invariant it relies on is st.files == 0 here.
}
