package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Moving an app manifest aside for the stale-manifest retry (steamrecovery.go).
//
// What goes aside is what an operator was told to delete by hand for the same
// failure: steamapps/appmanifest_<app>.acf, plus the app's download state under
// steamapps/downloading/ (its <app> dir and its state_<app>_*.patch files) so a
// leftover from an older target cannot steer the fresh install. It goes into a
// dir beside them in steamapps, which SteamCMD does not read (it loads only
// appmanifest_*.acf from there), so every move is a rename on one volume.

// manifestAsidePrefix names the dir holding a moved manifest for one pass.
const manifestAsidePrefix = ".kraken-steamcmd-aside-"

// appManifestMaxDepth bounds the search for a manifest below the data dir:
// every bundled spec installs into the data dir itself, and a spec with its own
// force_install_dir folder puts steamapps a level or two down.
const appManifestMaxDepth = 4

// findAppManifest returns the steamapps dir holding appmanifest_<appID>.acf
// under root, "" when there is none. More than one is an error: which tree the
// pass installed into is then not something to guess.
func findAppManifest(root, appID string) (string, error) {
	name := "appmanifest_" + appID + ".acf"
	direct := filepath.Join(root, "steamapps")
	if fi, err := os.Stat(filepath.Join(direct, name)); err == nil && fi.Mode().IsRegular() {
		return direct, nil
	}
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (isRestoreScratch(d.Name()) || strings.HasPrefix(d.Name(), manifestAsidePrefix)) {
				return filepath.SkipDir
			}
			if rel, rerr := filepath.Rel(root, p); rerr == nil && rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= appManifestMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && d.Name() == name && strings.EqualFold(filepath.Base(filepath.Dir(p)), "steamapps") {
			found = append(found, filepath.Dir(p))
		}
		return nil
	})
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d copies of %s in the data dir", len(found), name)
}

func (h hostStagingTree) setManifestAside(appID string) (manifestAside, error) {
	if h.root == "" {
		return nil, nil
	}
	steamapps, err := findAppManifest(h.root, appID)
	if err != nil || steamapps == "" {
		return nil, err
	}
	a := &hostAside{root: h.root, steamapps: steamapps, dir: filepath.Join(steamapps, manifestAsidePrefix+strconv.FormatInt(time.Now().UnixNano(), 10))}
	if err := os.Mkdir(a.dir, 0o755); err != nil {
		return nil, err
	}
	// The manifest first: it is the one move that has to happen. The download
	// state is moved too when there is any.
	items := []string{"appmanifest_" + appID + ".acf", filepath.Join("downloading", appID)}
	if patches, _ := filepath.Glob(filepath.Join(steamapps, "downloading", "state_"+appID+"_*.patch")); patches != nil {
		for _, p := range patches {
			items = append(items, filepath.Join("downloading", filepath.Base(p)))
		}
	}
	for i, rel := range items {
		from := filepath.Join(steamapps, rel)
		if i > 0 {
			if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}
		to := filepath.Join(a.dir, rel)
		err := os.MkdirAll(filepath.Dir(to), 0o755)
		if err == nil {
			err = os.Rename(from, to)
		}
		if err != nil {
			// Put back what already moved, so a failed move leaves the tree
			// as it was found.
			a.restore()
			return nil, fmt.Errorf("move %s aside: %w", a.relOf(from), err)
		}
		a.items = append(a.items, rel)
	}
	return a, nil
}

// hostAside is a manifestAside on the node's disk.
type hostAside struct {
	root      string   // the data dir
	steamapps string   // the steamapps dir the items came from
	dir       string   // the aside dir inside it
	items     []string // moved, relative to steamapps and to dir
}

func (a *hostAside) relOf(p string) string {
	if rel, err := filepath.Rel(a.root, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

func (a *hostAside) moved() []string {
	out := make([]string, 0, len(a.items))
	for _, rel := range a.items {
		out = append(out, a.relOf(filepath.Join(a.steamapps, rel)))
	}
	return out
}

func (a *hostAside) discard() error { return os.RemoveAll(a.dir) }

func (a *hostAside) restore() string {
	var back, kept, stuck []string
	for i := len(a.items) - 1; i >= 0; i-- {
		rel := a.items[i]
		orig := filepath.Join(a.steamapps, rel)
		if _, err := os.Lstat(orig); err == nil {
			// SteamCMD wrote its own since: newer than the one that failed.
			kept = append(kept, a.relOf(orig))
			continue
		}
		if err := os.Rename(filepath.Join(a.dir, rel), orig); err != nil {
			stuck = append(stuck, a.relOf(orig))
			continue
		}
		back = append(back, a.relOf(orig))
	}
	var parts []string
	if back != nil {
		parts = append(parts, "put back "+strings.Join(back, ", "))
	}
	if kept != nil {
		parts = append(parts, "kept the "+strings.Join(kept, ", ")+" SteamCMD wrote on the second pass")
	}
	if stuck != nil {
		parts = append(parts, "could not put back "+strings.Join(stuck, ", ")+"; the originals are in "+a.relOf(a.dir))
		return strings.Join(parts, "; ")
	}
	if err := os.RemoveAll(a.dir); err != nil {
		parts = append(parts, "could not remove "+a.relOf(a.dir)+": "+err.Error())
	}
	return strings.Join(parts, "; ")
}
