package agent

import (
	"context"
	"testing"
)

func fakeNames(t *testing.T, f *FakeRuntime, dir string) []string {
	t.Helper()
	ents, err := f.ListFiles(context.Background(), "s1", dir)
	if err != nil {
		t.Fatalf("ListFiles(%q): %v", dir, err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The fake data dir is a real tree: an upload shows up in the listing, a delete
// takes it away, and deleting a folder takes its contents (#287).
func TestFakeRuntime_FileTree(t *testing.T) {
	f := NewFakeRuntime("n1", "linux", false, "0.0.0")
	ctx := context.Background()

	if got := fakeNames(t, f, "."); !eq(got, []string{"saves", "server.cfg"}) {
		t.Fatalf("seeded root = %v", got)
	}
	// The relative form the UI sends for an upload into the root, and the
	// logical form the listing yields, both land under /data.
	if err := f.WriteFile(ctx, "s1", "./diag.dll", []byte("MZ")); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile(ctx, "s1", "/data/saves/backup.sav", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := fakeNames(t, f, "."); !eq(got, []string{"saves", "diag.dll", "server.cfg"}) {
		t.Fatalf("root after upload = %v", got)
	}
	if got := fakeNames(t, f, "/data/saves"); !eq(got, []string{"backup.sav", "world.sav"}) {
		t.Fatalf("saves after upload = %v", got)
	}
	data, size, _, _, err := f.ReadFile(ctx, "s1", "/data/diag.dll", 0)
	if err != nil || string(data) != "MZ" || size != 2 {
		t.Fatalf("ReadFile = %q %d %v", data, size, err)
	}

	if err := f.DeletePaths(ctx, "s1", []string{"/data/diag.dll"}); err != nil {
		t.Fatal(err)
	}
	if got := fakeNames(t, f, "."); !eq(got, []string{"saves", "server.cfg"}) {
		t.Fatalf("root after delete = %v", got)
	}
	if err := f.DeletePaths(ctx, "s1", []string{"/data/saves"}); err != nil {
		t.Fatal(err)
	}
	if got := fakeNames(t, f, "."); !eq(got, []string{"server.cfg"}) {
		t.Fatalf("root after folder delete = %v", got)
	}
	if _, err := f.ListFiles(ctx, "s1", "/data/saves"); err == nil {
		t.Fatal("listing a deleted folder should fail")
	}
	// The root is never deletable, and a missing path is an error the Panel
	// surfaces rather than a silent success.
	if err := f.DeletePaths(ctx, "s1", []string{"/data"}); err != nil {
		t.Fatalf("root delete should be a no-op, got %v", err)
	}
	if err := f.DeletePaths(ctx, "s1", []string{"/data/nope"}); err == nil {
		t.Fatal("deleting a missing path should fail")
	}
	// Servers keep separate trees.
	if got := fakeNames(t, f, "."); !eq(got, []string{"server.cfg"}) {
		t.Fatalf("s1 root = %v", got)
	}
	ents, err := f.ListFiles(ctx, "s2", ".")
	if err != nil || len(ents) != 2 {
		t.Fatalf("s2 seeded = %v %v", ents, err)
	}
}
