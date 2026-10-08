package agent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/steam"
)

// The Steam build check (#392): ask SteamCMD which build each app's branch
// ships right now, so the Panel can compare it with the build in a server's
// appmanifest and skip the multi-minute `app_update … validate` pass when the
// two already match.
//
// It runs in a one-shot container from the spec's own image (so SteamCMD is
// the same binary the install pass uses, and the image is normally already on
// the node), and it mounts NOTHING of a server's. That is the whole reason it
// needs neither the install gate (installgate.go) nor the data-dir guard
// (datadirguard.go): no data dir is bound, so there is no tree for it to race
// with and nothing a game container could be holding.

// appInfoContainerName is the check's one-shot container. It is a fixed name,
// not one per request, so a leftover from a crashed Agent is always found and
// cleared by the next check rather than accumulating; runs on one node are
// serialized to share it (appInfoChecks.run).
const appInfoContainerName = "kraken_appinfo"

// appInfoRole is the check container's labelRole value. The label, not the
// name, is what tells the rest of the Agent this managed container is a
// helper and not a server's game container.
const appInfoRole = "appinfo"

// appInfoRunTimeout bounds one SteamCMD session, container start to exit. A
// session is about 20 seconds on Linux, SteamCMD's own self-update included,
// plus a second per app; a Hyper-V Windows container adds its boot. Three
// minutes covers both with room for a slow Steam. A var only so tests can
// shorten it.
var appInfoRunTimeout = 3 * time.Minute

// appInfoMemory is the check container's memory limit. SteamCMD printing app
// info needs little, but a Hyper-V-isolated Windows container sizes its
// utility VM from this, and Server Core with PowerShell (the self-update
// guard's wait) does not boot comfortably in less than a gigabyte.
func (d *DockerRuntime) appInfoMemory() int64 {
	if d.isWindows() {
		return 1 << 30
	}
	return 512 << 20
}

// appInfoTailBytes is how much of SteamCMD's output goes back as raw_tail: the
// end of the session, where a login failure or a cut-off block shows.
const appInfoTailBytes = 4096

// appInfoMaxOutput caps the stdout kept for parsing. Seven apps print about
// 25 KB, so this is generous for any fleet; it only stops a runaway session
// from growing the Agent's memory without bound.
const appInfoMaxOutput = 16 << 20

// appInfoChecks is the Agent's state for build checks: the in-flight sessions,
// keyed so a second identical request joins the first, and the lock that keeps
// two sessions from fighting over appInfoContainerName.
type appInfoChecks struct {
	mu       sync.Mutex
	inflight map[string]*appInfoFlight
	// run is held for a whole check, sessions and container removal both.
	run sync.Mutex
	// wg counts checks still running (their container's removal included), so
	// a test can wait for one to finish touching the fake daemon.
	wg sync.WaitGroup
}

// appInfoFlight is one check, shared by every caller asking for the same image
// and app set. Its fields are written before done is closed, so a reader that
// has taken the channel may read them.
type appInfoFlight struct {
	done chan struct{}
	// apps is what SteamCMD said, merged across the session and its retry;
	// notes says, per app id, why an app is not in apps or has no branches
	// worth reading.
	apps  map[string]steam.AppInfo
	notes map[string]string
	tail  string
	err   error
}

// AppBuilds answers GetAppBuilds: the current build of each requested app and
// branch, from one SteamCMD session for the whole request.
//
// The contract the Panel decides a start on:
//
//   - Unavailable: Steam or the node could not be reached. SteamCMD printed
//     no app block at all (its login failed, the node has no network, the
//     session ran out of time first), or the check container would not be
//     created, started or followed. The message's last line quotes the end
//     of SteamCMD's output when there is any.
//   - FailedPrecondition: the request cannot run here (the image is not on
//     this node, or the platform is not this daemon's). InvalidArgument: the
//     request is malformed.
//   - A per-app error in the response: Steam answered, and that app is the
//     problem (an unknown id, a branch the anonymous account cannot see, a
//     block cut short or missing among apps that did parse), with the end of
//     SteamCMD's output in raw_tail.
func (d *DockerRuntime) AppBuilds(ctx context.Context, req *agentpb.GetAppBuildsRequest) (*agentpb.GetAppBuildsResponse, error) {
	if len(req.GetApps()) == 0 {
		return &agentpb.GetAppBuildsResponse{}, nil
	}
	img := strings.TrimSpace(req.GetImage())
	if img == "" {
		return nil, grpcstatus.Error(codes.InvalidArgument, "a steam build check needs the image that carries steamcmd")
	}
	ids, err := appInfoIDs(req.GetApps())
	if err != nil {
		return nil, err
	}
	windows, err := d.appInfoPlatform(req.GetPlatformType())
	if err != nil {
		return nil, err
	}

	f := d.joinAppInfoCheck(img, windows, ids)
	// An answer that is already there wins over a context that has also
	// ended: a select with both ready picks at random, and would throw the
	// answer away half the time.
	select {
	case <-f.done:
	default:
		select {
		case <-f.done:
		case <-ctx.Done():
			// The check itself carries on (it is shared, and bounded on its
			// own); only this caller stops waiting for it.
			return nil, fmt.Errorf("waiting for the steam build check: %w", ctx.Err())
		}
	}
	if f.err != nil {
		return nil, f.err
	}

	resp := &agentpb.GetAppBuildsResponse{RawTail: f.tail}
	for _, q := range req.GetApps() {
		resp.Builds = append(resp.Builds, appBuildFor(q, f))
	}
	return resp, nil
}

// appInfoIDs returns the request's distinct app ids, sorted, refusing any that
// is not a plain number. The ids are spliced into a shell command line, so
// this check is what keeps a request from carrying anything else into it.
func appInfoIDs(apps []*agentpb.AppBuildQuery) ([]string, error) {
	ids := make([]string, 0, len(apps))
	for _, q := range apps {
		id := q.GetAppId()
		if !isSteamAppID(id) {
			return nil, grpcstatus.Errorf(codes.InvalidArgument, "steam app id %q is not a number", id)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func isSteamAppID(s string) bool {
	if s == "" || len(s) > 10 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// appInfoPlatform resolves the request's platform against this node's daemon.
// The image is the spec's image for that platform, so a Windows image asked of
// a Linux daemon (or the reverse) cannot run here at all; that is the Panel
// picking the wrong node, and it is told so rather than handed a pull error.
// An empty platform means the daemon's own.
func (d *DockerRuntime) appInfoPlatform(platform string) (windows bool, err error) {
	p := strings.ToLower(strings.TrimSpace(platform))
	daemonWindows := d.isWindows()
	if p == "" {
		return daemonWindows, nil
	}
	wantWindows := strings.HasPrefix(p, "windows")
	if wantWindows != daemonWindows {
		return false, grpcstatus.Errorf(codes.FailedPrecondition,
			"this node runs %s containers; a %s steam build check cannot run here", d.OSType(), p)
	}
	return wantWindows, nil
}

// joinAppInfoCheck returns the in-flight check for this image and app set,
// starting one if there is none. A daily fleet check and an operator's "Check
// now" asking about the same apps then share one SteamCMD session, the way
// backgroundPull shares one pull. Branches are not part of the key: one
// app_info_print reports every branch of an app.
func (d *DockerRuntime) joinAppInfoCheck(img string, windows bool, ids []string) *appInfoFlight {
	key := fmt.Sprintf("%s|%t|%s", img, windows, strings.Join(ids, ","))
	c := &d.appInfo
	c.mu.Lock()
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		return f
	}
	f := &appInfoFlight{done: make(chan struct{})}
	if c.inflight == nil {
		c.inflight = map[string]*appInfoFlight{}
	}
	c.inflight[key] = f
	c.wg.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.wg.Done()
		// Detached from any caller's context: the check is shared, so one
		// caller giving up must not cancel it for the others. The pull and
		// the session each carry their own bound.
		d.runAppInfoCheck(img, windows, ids, f, func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
			close(f.done)
		})
	}()
	return f
}

// runAppInfoCheck does one check: make the image available, run SteamCMD, and
// once more for any app whose block came back without branches. publish hands
// the result to the waiting callers; the container is removed after that, off
// their path but still under the run lock, so the next check never meets it.
func (d *DockerRuntime) runAppInfoCheck(img string, windows bool, ids []string, f *appInfoFlight, publish func()) {
	apps := strings.Join(ids, ",")
	note := func(line string) {
		slog.Info("[kraken] steam build check: "+line, "image", img, "apps", apps)
	}
	began := time.Now()
	published := false
	finish := func() {
		if !published {
			published = true
			publish()
		}
	}
	defer finish()

	// Outside the run lock, so a slow registry for one image does not hold
	// up a check of another.
	imageBegan := time.Now()
	if err := d.appInfoImage(img); err != nil {
		f.err = err
		return
	}
	note("image check took " + took(imageBegan))

	c := &d.appInfo
	c.run.Lock()
	defer c.run.Unlock()

	first, err := d.appInfoSession(img, windows, ids, note)
	if err != nil {
		f.err = err
		finish()
		d.removeAppInfoContainer(first.id)
		return
	}
	// A session that printed no app block at all never got through to Steam:
	// the login failed, the node has no network, or it ran out of time first.
	// That is the check failing, not every app at once, and it is the RPC's
	// error (Unavailable) so the Panel can tell "Steam could not be asked"
	// from "Steam answered and this app is the problem". It is not retried:
	// a second session would only do the same again.
	if !first.parsed {
		f.err = appInfoUnreachable(first)
		note(grpcstatus.Convert(f.err).Message())
		finish()
		d.removeAppInfoContainer(first.id)
		return
	}
	f.apps, f.notes = map[string]steam.AppInfo{}, map[string]string{}
	f.tail = first.output
	mergeAppInfo(f, ids, first)

	// SteamCMD on a fresh home can print an app's block before its client
	// config has loaded, without the branches; asking again in a new session
	// answers it. Only when the first session got through to Steam at all
	// (it printed some app's block) and finished: a session that never
	// reached Steam, or ran out of time, would only do the same again.
	if retry := appInfoMissing(f, ids); len(retry) > 0 && first.parsed && !first.timedOut {
		d.removeAppInfoContainer(first.id)
		note("no branches in steamcmd's answer for " + strings.Join(retry, ",") + "; asking once more")
		again, err := d.appInfoSession(img, windows, retry, note)
		if err == nil {
			f.tail = first.output + again.output
			mergeAppInfo(f, retry, again)
		} else {
			note("the second session failed: " + err.Error())
		}
		first.id = again.id
	}
	f.tail = lastBytes(f.tail, appInfoTailBytes)
	note("check took " + took(began))

	finish()
	d.removeAppInfoContainer(first.id)
}

// appInfoUnreachable is the RPC error for a session that printed no app block
// at all. Its last line is the end of what SteamCMD printed, ANSI stripped,
// so the reason ("FAILED (No Connection)") reaches whoever reads the error
// without a trip to the node; raw_tail is not there to carry it, since an
// error answer has no response body.
func appInfoUnreachable(res appInfoResult) error {
	why := fmt.Sprintf("steamcmd printed no app info at all (exit %d): its login failed or Steam was unreachable from this node", res.exit)
	if res.timedOut {
		why = fmt.Sprintf("steamcmd did not finish within %s and printed no app info: Steam was unreachable or too slow from this node", appInfoRunTimeout)
	}
	msg := "steam build check: " + why
	if last := appInfoLastLines(res.output, appInfoErrorTailBytes); last != "" {
		msg += "\nsteamcmd's last output: " + last
	}
	return grpcstatus.Error(codes.Unavailable, msg)
}

// appInfoErrorTailBytes bounds the output quoted in an Unavailable error. It
// is shorter than raw_tail: the message ends up in the Panel's check error and
// its log line, where a few lines say what happened and 4 KB would bury it.
const appInfoErrorTailBytes = 512

// appInfoLastLines returns the last non-empty lines of out, ANSI stripped,
// joined with " | " on one line, at most about n bytes of them.
func appInfoLastLines(out string, n int) string {
	lines := strings.Split(steam.StripANSI(out), "\n")
	var kept []string
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if size+len(l) > n && len(kept) > 0 {
			break
		}
		if len(l) > n {
			l = "…" + l[len(l)-n:]
		}
		kept = append(kept, l)
		size += len(l) + 3
	}
	slices.Reverse(kept)
	return strings.Join(kept, " | ")
}

// appInfoImage makes sure the check can run on img without turning it into a
// download.
//
// An image that is not on the node fails the check at once. The Panel sends a
// check to a node that hosts the game, so a missing image means it picked
// wrong, and pulling on the check's behalf would let a daily fleet check start
// a multi-gigabyte kraken-steam-win download on a node that never ran the game,
// holding the in-flight check for as long as that takes.
//
// An image that IS here is refreshed the way an operator start refreshes it
// (refreshImageForStart): the pull policy decides whether to ask the
// registry, the check waits startPullBudget for an unchanged tag's manifest
// check, and a tag that really moved finishes downloading in the background
// while this check runs on the local copy. Build ids come from Steam, not from
// the image, so an older SteamCMD answers the question just as well.
func (d *DockerRuntime) appInfoImage(img string) error {
	ctx := context.Background()
	if _, err := d.images.ImageInspect(ctx, img); err != nil {
		return grpcstatus.Errorf(codes.FailedPrecondition,
			"steam build check: image %s is not on this node; check from a node that hosts the game", img)
	}
	// The server id is only a log field there; the container name says
	// which caller this is.
	if err := d.refreshImageForStart(ctx, img, appInfoContainerName); err != nil {
		return grpcstatus.Errorf(codes.FailedPrecondition, "steam build check: %v", err)
	}
	return nil
}

// appInfoResult is one SteamCMD session's outcome.
type appInfoResult struct {
	id       string // the container, for removal
	output   string // stdout and stderr in arrival order
	apps     map[string]steam.AppInfo
	parsed   bool // stdout held at least one app block
	exit     int64
	timedOut bool
}

// appInfoSession runs one SteamCMD session for ids in a fresh check container
// and parses what it printed. The error is for a session that could not run
// (the container would not be created or started, the log stream broke); a
// session that ran and printed nothing useful is a result, with the reason in
// it.
func (d *DockerRuntime) appInfoSession(img string, windows bool, ids []string, note func(string)) (appInfoResult, error) {
	var res appInfoResult
	ctx, cancel := context.WithTimeout(context.Background(), appInfoRunTimeout)
	defer cancel()

	if err := d.clearAppInfoName(ctx); err != nil {
		return res, grpcstatus.Errorf(codes.Unavailable, "steam build check: %v", err)
	}
	script, guarded := appInfoScript(windows, ids)
	if guarded {
		note("windows SteamCMD guard applied: priming steamcmd.exe's self-update and waiting for steamcmd to exit before the check container ends")
	}
	cfg := &container.Config{
		Image:      img,
		Entrypoint: d.shellEntrypoint(),
		Cmd:        []string{script},
		Labels:     map[string]string{labelManaged: "true", labelRole: appInfoRole},
	}
	// No Binds, no ports: the check reads nothing of a server's and serves
	// nothing. See the top of this file.
	host := &container.HostConfig{}
	host.Resources.Memory = d.appInfoMemory()
	d.applyIsolation(host)

	created, err := d.containers.ContainerCreate(ctx, cfg, host, nil, nil, appInfoContainerName)
	if err != nil {
		return res, grpcstatus.Errorf(codes.Unavailable, "steam build check: create %s: %v", appInfoContainerName, err)
	}
	res.id = created.ID
	if err := d.containers.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return res, grpcstatus.Errorf(codes.Unavailable, "steam build check: start %s: %v", appInfoContainerName, err)
	}
	note("running steamcmd for apps " + strings.Join(ids, ","))
	ranFrom := time.Now()

	// demux scans stdout and stderr on two goroutines. Only stdout is parsed:
	// a stderr line (steamcmd.sh's "Restarting steamcmd by request...")
	// landing between two lines of a block would break it. Both go to the
	// tail, in the order they arrived, since that is what an operator reads.
	var mu sync.Mutex
	var stdout, all strings.Builder
	streamErr := d.streamLogs(ctx, created.ID, "all", func(stream, text string) error {
		mu.Lock()
		defer mu.Unlock()
		if stream == "stdout" && stdout.Len() < appInfoMaxOutput {
			stdout.WriteString(text)
			stdout.WriteByte('\n')
		}
		all.WriteString(text)
		all.WriteByte('\n')
		if all.Len() > 4*appInfoTailBytes {
			tail := lastBytes(all.String(), appInfoTailBytes)
			all.Reset()
			all.WriteString(tail)
		}
		return nil
	})
	if streamErr != nil && ctx.Err() == nil {
		return res, grpcstatus.Errorf(codes.Unavailable, "steam build check: stream %s logs: %v", appInfoContainerName, streamErr)
	}

	// timedOut is set only where the deadline is what ended the wait. An exit
	// status that arrived right on the deadline is a finished session, so it
	// is not read off ctx.Err() after the fact: that would suppress the retry
	// and tell the Panel SteamCMD never finished.
	statusCh, errCh := d.containers.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case st := <-statusCh:
		res.exit = st.StatusCode
		note(fmt.Sprintf("steamcmd ran %s, exit %d", took(ranFrom), st.StatusCode))
	case werr := <-errCh:
		if ctx.Err() == nil {
			return res, grpcstatus.Errorf(codes.Unavailable, "steam build check: wait for %s: %v", appInfoContainerName, werr)
		}
		// The client ends the wait with the context's own error when the
		// deadline passes: the same timeout as the case below, by another
		// channel.
		res.timedOut = true
	case <-ctx.Done():
		res.timedOut = true
	}
	if res.timedOut {
		note(fmt.Sprintf("steamcmd did not finish within %s; reading what it printed", appInfoRunTimeout))
	}

	mu.Lock()
	res.output = all.String()
	parsed, perr := steam.ParseAppInfo(stdout.String())
	mu.Unlock()
	res.apps, res.parsed = parsed, perr == nil
	if perr != nil {
		note("no app info in steamcmd's output: " + perr.Error())
	}
	return res, nil
}

// appInfoScript is the SteamCMD command line for ids: log in anonymously,
// refresh the app info cache, print each app, quit. Every bundled Steam spec
// installs anonymously, so no credentials are involved.
//
// On Windows it goes through the same self-update guard as an install
// (steamguard.go). The trap it guards against is the binary's, not the
// install's: steamcmd.exe relaunches itself after updating and the first
// process exits, which would end the container before the relaunched one
// printed anything.
func appInfoScript(windows bool, ids []string) (script string, guarded bool) {
	bin := "steamcmd"
	if windows {
		bin = "steamcmd.exe"
	}
	var b strings.Builder
	b.WriteString(bin + " +login anonymous +app_info_update 1")
	for _, id := range ids {
		b.WriteString(" +app_info_print " + id)
	}
	b.WriteString(" +quit")
	if !windows {
		return b.String(), false
	}
	return guardWindowsSteamInstall(b.String())
}

// mergeAppInfo folds one session's apps into the flight. An app the session
// read with branches replaces whatever an earlier session said; one it read
// without them is kept only if nothing better is there yet.
func mergeAppInfo(f *appInfoFlight, ids []string, res appInfoResult) {
	for _, id := range ids {
		a, ok := res.apps[id]
		if !ok {
			if _, had := f.apps[id]; !had {
				f.notes[id] = appInfoAbsentNote(id, res)
			}
			continue
		}
		if prev, had := f.apps[id]; had && prev.Branches != nil && a.Branches == nil {
			continue
		}
		f.apps[id] = a
		delete(f.notes, id)
	}
}

// appInfoAbsentNote says why an app has no block in a session's output, as
// precisely as the session lets us.
func appInfoAbsentNote(id string, res appInfoResult) string {
	switch {
	case res.timedOut:
		return fmt.Sprintf("steamcmd did not finish within %s and printed no app info for app %s", appInfoRunTimeout, id)
	case !res.parsed:
		return fmt.Sprintf("steamcmd printed no app info at all (exit %d): login failed or Steam was unreachable; see the raw output", res.exit)
	}
	return fmt.Sprintf("steamcmd printed no app info for app %s (exit %d)", id, res.exit)
}

// appInfoMissing lists the apps worth asking about again: absent from the
// output, or a block without its "branches" key. An empty block is left out:
// that is SteamCMD's verified answer for an app id Steam does not know, and
// asking again would only cost every check of a mistyped spec a second
// session.
func appInfoMissing(f *appInfoFlight, ids []string) []string {
	var out []string
	for _, id := range ids {
		if a, ok := f.apps[id]; !ok || (a.Branches == nil && !a.Unknown) {
			out = append(out, id)
		}
	}
	return out
}

// appBuildFor answers one query from the flight. An empty branch is "public",
// the branch every bundled spec installs; the response echoes the branch as
// asked, so the Panel matches answers to questions by what it sent.
func appBuildFor(q *agentpb.AppBuildQuery, f *appInfoFlight) *agentpb.AppBuild {
	out := &agentpb.AppBuild{AppId: q.GetAppId(), Branch: q.GetBranch()}
	branch := q.GetBranch()
	if branch == "" {
		branch = "public"
	}
	a, ok := f.apps[q.GetAppId()]
	switch {
	case !ok:
		out.Error = f.notes[q.GetAppId()]
		if out.Error == "" {
			out.Error = "steamcmd printed no app info for app " + q.GetAppId()
		}
		return out
	case a.Unknown:
		out.Error = fmt.Sprintf("steam returned no app info for app %s: the id is unknown, or the anonymous account cannot see it", a.AppID)
		return out
	case a.Branches == nil && a.Truncated:
		out.Error = fmt.Sprintf("steamcmd's output for app %s was cut off before its branches", a.AppID)
		return out
	case a.Branches == nil:
		out.Error = fmt.Sprintf("steamcmd returned app %s without its branches, twice", a.AppID)
		return out
	}
	b, ok := a.Branches[branch]
	if !ok && a.Truncated {
		out.Error = fmt.Sprintf("steamcmd's output for app %s was cut off before branch %q", a.AppID, branch)
		return out
	}
	if !ok {
		names := make([]string, 0, len(a.Branches))
		for n := range a.Branches {
			names = append(names, n)
		}
		slices.Sort(names)
		out.Error = fmt.Sprintf("app %s has no branch %q that the anonymous account can see (branches: %s)", a.AppID, branch, strings.Join(names, ", "))
		return out
	}
	if b.BuildID == "" {
		out.Error = fmt.Sprintf("app %s branch %q carries no buildid", a.AppID, branch)
		return out
	}
	out.BuildId, out.TimeUpdated = b.BuildID, b.TimeUpdated
	return out
}

// clearAppInfoName frees appInfoContainerName before a session creates it.
// Within one Agent the run lock means nothing else is using it, so whatever
// holds it is a leftover: a check whose removal did not land, or one a crashed
// Agent left running. Either is removed, running or not, since nothing of a
// server's is mounted in it. A container under that name that does not carry
// the check's role label, or that is labelled as a server's, is not ours to
// remove, and the check refuses instead.
func (d *DockerRuntime) clearAppInfoName(ctx context.Context) error {
	info, err := d.containers.ContainerInspect(ctx, appInfoContainerName)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", appInfoContainerName, err)
	}
	labels := map[string]string{}
	if info.Config != nil {
		labels = info.Config.Labels
	}
	if labels[labelManaged] != "true" || labels[labelRole] != appInfoRole || labels[labelServerID] != "" {
		return fmt.Errorf("container %s holds the build check's name and was not left by a build check; refusing to remove it — rename or remove it", appInfoContainerName)
	}
	return d.removeAndAwaitName(ctx, info.ID, appInfoContainerName)
}

// removeAppInfoContainer removes a finished check container and waits for its
// name. A removal that does not land is logged; the next check clears it.
func (d *DockerRuntime) removeAppInfoContainer(id string) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), installRemoveTimeout)
	defer cancel()
	if err := d.removeAndAwaitName(ctx, id, appInfoContainerName); err != nil {
		slog.Warn("steam build check container did not clear; the next check will clear it",
			"name", appInfoContainerName, "err", err)
	}
}

// lastBytes returns the last n bytes of s, starting at a line boundary when
// one falls inside them, so the tail does not open on half a line.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return s
}
