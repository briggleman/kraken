package api

import (
	"context"
	"slices"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// A backup is the game's SAVE DATA, not the reinstallable install tree (#218).
// The Panel owns that policy and resolves it into concrete globs here, on every
// CreateBackup — manual and scheduled alike — so the Agent never needs the spec
// and the policy is versioned with the Panel that ships it.

// builtinBackupExcludes is the fallback policy for a spec with NO `backup:`
// block: capture the whole data dir MINUS the entries below. Deliberately not
// save-dir guessing — an include list that misses the saves produces a green
// backup with nothing in it, the worst failure this system has — so every entry
// here must be unambiguously ephemeral or re-downloadable, and each carries the
// reason it qualifies. Paths are matched against data-dir-relative POSIX paths.
//
// SteamCMD itself is NOT listed: the images keep it at /home/steam/steamcmd
// (baked in from kraken-steam-deps), never inside /data, so there is nothing to
// exclude. steamapps/workshop/content is also NOT listed — installed Workshop
// items can hold operator-curated mod content, and re-downloadable is not the
// same as disposable.
var builtinBackupExcludes = []string{
	// SteamCMD's partial-chunk staging area for an in-flight app_update. Torn
	// by definition, and discarded + re-fetched by the next update.
	"steamapps/downloading/**",
	// SteamCMD's scratch space for download/validate. Same lifetime as above.
	"steamapps/temp/**",
	// In-flight Workshop item downloads — the staging twin of downloading/.
	"steamapps/workshop/downloads/**",
	// Steam's compiled-shader cache: a cache, regenerated on demand, and inert
	// for a headless dedicated server that renders nothing.
	"steamapps/shadercache/**",
	// Server logs. The enshrouded spec points logDirectory at ./logs and vrising
	// at C:\data\logs, and these are the files that GROW mid-archive — the direct
	// cause of the "archive/tar: write too long" backup failures.
	"**/logs/**",
	// Same, spelled the Unreal Engine way: <Project>/Saved/Logs.
	"**/Logs/**",
	// Any stray log file outside a logs dir (UE's <Project>.log, SteamCMD's own
	// output): text, append-only, and regenerated every run.
	"**/*.log",
	// Crash minidumps: post-mortem artifacts, never game state.
	"**/*.dmp",
	"**/*.mdmp",
	// Windows/Unity crash-dump directory.
	"**/CrashDumps/**",
	// Unreal's per-project crash-report directory (<Project>/Saved/Crashes).
	"**/Crashes/**",
}

// backupGlobsFor resolves the include/exclude globs for a backup of sp.
//
// A spec that declares a `backup:` block has already answered the question the
// built-ins only guess at, so the block is the WHOLE policy — the built-in
// excludes are not merged in. Quietly adding excludes underneath an author's
// include list would mean a spec cannot express "capture all of this", and an
// include list is already narrow by construction.
//
// A nil spec (unreadable, mid-delete) resolves like a spec with no block: the
// built-in excludes only drop ephemera, so it is the safe answer.
func backupGlobsFor(sp *spec.Spec) (include, exclude []string) {
	if sp != nil && sp.Backup != nil {
		inc, exc := sp.Backup.Patterns()
		return slices.Clone(inc), slices.Clone(exc)
	}
	return nil, slices.Clone(builtinBackupExcludes)
}

// backupRequestFor builds the CreateBackup request for sv: one spec load feeds
// both the slug (which expands the node's backup-path tokens) and the globs.
func (s *Server) backupRequestFor(ctx context.Context, sv *store.Server, name string) *agentpb.CreateBackupRequest {
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if err != nil {
		// Don't refuse the backup: the slug only decorates the target path and
		// the built-in fallback still keeps the growing logs out of the tar.
		s.logger.Warn("backup: spec unavailable; falling back to the built-in glob policy", "server", sv.ID, "spec", sv.SpecID, "err", err)
		sp = nil
	}
	req := &agentpb.CreateBackupRequest{ServerId: sv.ID, Name: name}
	if sp != nil {
		req.Slug = sp.Slug
	}
	req.BackupInclude, req.BackupExclude = backupGlobsFor(sp)
	return req
}
