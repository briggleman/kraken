package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An archive is published only when it is whole (#360 review). A write that
// dies before the rename — a copy that fails, or an Agent that is killed
// mid-copy, which leaves the .partial behind — must never list as an archive:
// with the job tracker gone, a listed file reads as READY, and a retire would
// then delete the world with half a backup left.

type cutShortReader struct{ n int }

func (r *cutShortReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("agent killed mid-copy")
	}
	k := min(len(p), r.n)
	r.n -= k
	return k, nil
}

func TestLocalPut_AnInterruptedWriteIsNeverListed(t *testing.T) {
	ctx := context.Background()
	for _, target := range []backupTarget{
		&localBackupTarget{dir: t.TempDir()},
		&shareBackupTarget{localBackupTarget{dir: t.TempDir(), flat: true}},
	} {
		t.Run(target.Kind(), func(t *testing.T) {
			const id = "1700000000000__final-before-retire"
			// A copy that fails part-way: nothing is published, nothing is left.
			if err := target.Put(ctx, "sv-1", id, &cutShortReader{n: 4096}, 0); err == nil {
				t.Fatal("a failed copy reported success")
			}
			if list, err := target.List(ctx, "sv-1"); err != nil || len(list) != 0 {
				t.Fatalf("after a failed copy: list %+v err %v, want nothing", list, err)
			}

			// An Agent killed mid-copy: the .partial is on disk, the rename
			// never happened. It is not an archive.
			var dir string
			switch tt := target.(type) {
			case *localBackupTarget:
				dir = tt.serverDir("sv-1")
			case *shareBackupTarget:
				dir = tt.serverDir("sv-1")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			stale := filepath.Join(dir, id+".tar.gz"+partialSuffix)
			if err := os.WriteFile(stale, []byte("half an archive"), 0o644); err != nil {
				t.Fatal(err)
			}
			if list, err := target.List(ctx, "sv-1"); err != nil || len(list) != 0 {
				t.Fatalf("with a stale .partial: list %+v err %v, want nothing", list, err)
			}

			// The next Put of the same id replaces it and publishes whole.
			if err := target.Put(ctx, "sv-1", id, strings.NewReader("the whole archive"), 0); err != nil {
				t.Fatalf("put: %v", err)
			}
			list, err := target.List(ctx, "sv-1")
			if err != nil || len(list) != 1 || list[0].Id != id || list[0].Size != int64(len("the whole archive")) {
				t.Fatalf("after a good put: %+v %v", list, err)
			}
			if _, err := os.Stat(stale); !os.IsNotExist(err) {
				t.Fatalf("the stale .partial survived the next put: %v", err)
			}
			r, err := target.Open(ctx, "sv-1", id)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r)
			_ = r.Close()
			if string(b) != "the whole archive" {
				t.Fatalf("published archive = %q", b)
			}
		})
	}
}
