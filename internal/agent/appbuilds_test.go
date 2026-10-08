package agent

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// appInfoOps wraps fakeOps to record what each check container was created
// with, the order of creates and removals, and to hold a session's log stream
// open until the test releases it.
type appInfoOps struct {
	*fakeOps

	mu      sync.Mutex
	cfgs    []*container.Config
	hosts   []*container.HostConfig
	events  []string      // "create <name>", "remove <id>"
	gate    chan struct{} // when set, ContainerLogs blocks until it is closed
	entered chan struct{} // when set, signalled as a session's logs are opened
}

func (a *appInfoOps) ContainerCreate(ctx context.Context, cfg *container.Config, host *container.HostConfig, n *network.NetworkingConfig, p *ocispec.Platform, name string) (container.CreateResponse, error) {
	a.mu.Lock()
	a.cfgs = append(a.cfgs, cfg)
	a.hosts = append(a.hosts, host)
	a.events = append(a.events, "create "+name)
	a.mu.Unlock()
	return a.fakeOps.ContainerCreate(ctx, cfg, host, n, p, name)
}

func (a *appInfoOps) ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error {
	a.mu.Lock()
	a.events = append(a.events, "remove "+id)
	a.mu.Unlock()
	return a.fakeOps.ContainerRemove(ctx, id, opts)
}

func (a *appInfoOps) ContainerLogs(ctx context.Context, id string, opts container.LogsOptions) (io.ReadCloser, error) {
	if a.entered != nil {
		a.entered <- struct{}{}
	}
	if a.gate != nil {
		<-a.gate
	}
	return a.fakeOps.ContainerLogs(ctx, id, opts)
}

func (a *appInfoOps) snapshot() (cfgs []*container.Config, hosts []*container.HostConfig, events []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.cfgs), slices.Clone(a.hosts), slices.Clone(a.events)
}

// newAppInfoRuntime is a Linux runtime whose image is already on the node and
// whose daemon replays sessions, in order, as each check container's output.
func newAppInfoRuntime(t *testing.T, sessions ...[]string) (*DockerRuntime, *appInfoOps) {
	t.Helper()
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	ops := &appInfoOps{fakeOps: &fakeOps{passLogs: sessions}}
	d.containers = ops
	t.Cleanup(d.appInfo.wg.Wait)
	return d, ops
}

// realSession is the captured SteamCMD session for the seven bundled apps
// (internal/shared/steam/testdata), line by line, ANSI resets included.
func realSession(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../shared/steam/testdata/appinfo_7apps.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func queries(pairs ...string) []*agentpb.AppBuildQuery {
	var out []*agentpb.AppBuildQuery
	for _, p := range pairs {
		id, branch, _ := strings.Cut(p, "@")
		out = append(out, &agentpb.AppBuildQuery{AppId: id, Branch: branch})
	}
	return out
}

const appInfoPreamble = "Loading Steam API...\x1b[0mOK\n" +
	"Connecting anonymously to Steam Public...\x1b[0mOK\n" +
	"\x1b[0mWaiting for user info...\x1b[0mOK"

func appBlock(id, body string) []string {
	return strings.Split("\x1b[0mAppID : "+id+", change number : 1/1, last change : Thu Oct  8 14:44:02 2026 \n\""+id+"\"\n{\n"+body+"}", "\n")
}

func branchesBody(build string) string {
	return "\t\"common\"\n\t{\n\t\t\"name\"\t\t\"x\"\n\t}\n\t\"depots\"\n\t{\n\t\t\"branches\"\n\t\t{\n\t\t\t\"public\"\n\t\t\t{\n" +
		"\t\t\t\t\"buildid\"\t\t\"" + build + "\"\n\t\t\t\t\"timeupdated\"\t\t\"1789441330\"\n\t\t\t}\n\t\t}\n\t}\n"
}

// The whole check is one SteamCMD session in a container named
// kraken_appinfo, from the requested image, with nothing of a server's
// mounted and no ports; the answers come back in request order.
func TestAppBuilds_OneSessionNothingMounted(t *testing.T) {
	d, ops := newAppInfoRuntime(t, realSession(t))
	resp, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{
		Image: testRef, PlatformType: "linux",
		Apps: queries("2394010", "896660@public", "4019830", "2857200", "2278520", "1829350", "4129620",
			"896660@default_old", "2394010@nosuchbranch"),
	})
	if err != nil {
		t.Fatalf("AppBuilds: %v", err)
	}
	d.appInfo.wg.Wait()

	want := []struct{ app, branch, build, err string }{
		{"2394010", "", "25247047", ""},
		{"896660", "public", "25730807", ""},
		{"4019830", "", "25805654", ""},
		{"2857200", "", "24343458", ""},
		{"2278520", "", "23178631", ""},
		{"1829350", "", "25169871", ""},
		{"4129620", "", "24913903", ""},
		{"896660", "default_old", "25527701", ""},
		{"2394010", "nosuchbranch", "", `no branch "nosuchbranch"`},
	}
	if len(resp.GetBuilds()) != len(want) {
		t.Fatalf("want %d answers, got %d: %v", len(want), len(resp.GetBuilds()), resp.GetBuilds())
	}
	for i, w := range want {
		b := resp.GetBuilds()[i]
		if b.GetAppId() != w.app || b.GetBranch() != w.branch || b.GetBuildId() != w.build {
			t.Errorf("answer %d = %v, want %s@%s build %s", i, b, w.app, w.branch, w.build)
		}
		if w.err == "" && b.GetError() != "" || w.err != "" && !strings.Contains(b.GetError(), w.err) {
			t.Errorf("answer %d error = %q, want %q", i, b.GetError(), w.err)
		}
	}
	if got := resp.GetBuilds()[0].GetTimeUpdated(); got != 1789441330 {
		t.Errorf("time_updated = %d, want Steam's timeupdated 1789441330", got)
	}

	cfgs, hosts, events := ops.snapshot()
	if len(cfgs) != 1 {
		t.Fatalf("want ONE session for every app, got %d", len(cfgs))
	}
	cfg, host := cfgs[0], hosts[0]
	if cfg.Image != testRef || !slices.Equal(cfg.Entrypoint, []string{"/bin/sh", "-c"}) {
		t.Errorf("image %q entrypoint %v", cfg.Image, cfg.Entrypoint)
	}
	script := strings.Join(cfg.Cmd, " ")
	if !strings.HasPrefix(script, "steamcmd +login anonymous +app_info_update 1 ") || !strings.HasSuffix(script, " +quit") {
		t.Errorf("script = %q", script)
	}
	for _, id := range []string{"2394010", "896660", "4019830", "2857200", "2278520", "1829350", "4129620"} {
		if n := strings.Count(script, "+app_info_print "+id+" "); n != 1 {
			t.Errorf("app %s printed %d times in %q, want once", id, n, script)
		}
	}
	if len(host.Binds) != 0 || len(host.Mounts) != 0 || len(host.PortBindings) != 0 || len(cfg.ExposedPorts) != 0 {
		t.Errorf("the check container must mount and publish nothing: binds=%v mounts=%v ports=%v exposed=%v",
			host.Binds, host.Mounts, host.PortBindings, cfg.ExposedPorts)
	}
	if host.Resources.Memory <= 0 {
		t.Error("the check container has no memory limit")
	}
	if cfg.Labels[labelManaged] != "true" || cfg.Labels[labelServerID] != "" {
		t.Errorf("labels = %v, want managed and no server id", cfg.Labels)
	}
	if !slices.Equal(events, []string{"create " + appInfoContainerName, "remove install-1"}) {
		t.Errorf("events = %v, want one create and its removal", events)
	}
	// Neither the install gate's nor the data-dir guard's machinery: nothing
	// listed the node's containers looking for a data-dir holder.
	if ops.lists != 0 {
		t.Errorf("the check listed containers %d times; it must not run the data-dir guard", ops.lists)
	}

	tail := resp.GetRawTail()
	if tail == "" || len(tail) > appInfoTailBytes || !strings.Contains(tail, "Unloading Steam API") {
		t.Errorf("raw_tail (%d bytes) should be the end of the session: %q", len(tail), tail)
	}
}

// An app whose block came back without branches (SteamCMD's fresh-home quirk)
// is asked about once more, alone, in a second session; the apps the first
// session answered are not asked again.
func TestAppBuilds_RetriesAppWithoutBranches(t *testing.T) {
	first := append(append([]string{appInfoPreamble}, appBlock("2394010", "\t\"common\"\n\t{\n\t}\n")...), appBlock("896660", branchesBody("25730807"))...)
	second := append([]string{appInfoPreamble}, appBlock("2394010", branchesBody("25247047"))...)
	d, ops := newAppInfoRuntime(t, first, second)

	resp, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("896660", "2394010")})
	if err != nil {
		t.Fatalf("AppBuilds: %v", err)
	}
	d.appInfo.wg.Wait()
	if b := resp.GetBuilds(); b[0].GetBuildId() != "25730807" || b[1].GetBuildId() != "25247047" || b[1].GetError() != "" {
		t.Errorf("builds = %v", b)
	}
	cfgs, _, events := ops.snapshot()
	if len(cfgs) != 2 {
		t.Fatalf("want a retry session, got %d sessions", len(cfgs))
	}
	if retry := strings.Join(cfgs[1].Cmd, " "); strings.Contains(retry, "896660") || !strings.Contains(retry, "+app_info_print 2394010") {
		t.Errorf("the retry should ask only for the app without branches: %q", retry)
	}
	// The first container is gone before the second takes its name.
	want := []string{"create " + appInfoContainerName, "remove install-1", "create " + appInfoContainerName, "remove install-2"}
	if !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

// Still no branches after the retry: the app gets an error and the others
// their builds.
func TestAppBuilds_NoBranchesTwiceIsAnAppError(t *testing.T) {
	noBranches := append([]string{appInfoPreamble}, appBlock("2394010", "\t\"common\"\n\t{\n\t}\n")...)
	d, _ := newAppInfoRuntime(t, noBranches, noBranches)
	resp, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010")})
	if err != nil {
		t.Fatal(err)
	}
	if e := resp.GetBuilds()[0].GetError(); !strings.Contains(e, "without its branches") {
		t.Errorf("error = %q", e)
	}
}

// An unknown app id (SteamCMD prints an empty block) is an app error and is
// not worth a second session.
func TestAppBuilds_UnknownAppIsNotRetried(t *testing.T) {
	session := append(append([]string{appInfoPreamble}, appBlock("999999999", "")...), appBlock("2394010", branchesBody("25247047"))...)
	d, ops := newAppInfoRuntime(t, session)
	resp, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("999999999", "2394010")})
	if err != nil {
		t.Fatal(err)
	}
	d.appInfo.wg.Wait()
	b := resp.GetBuilds()
	if !strings.Contains(b[0].GetError(), "unknown") || b[0].GetBuildId() != "" || b[1].GetBuildId() != "25247047" {
		t.Errorf("builds = %v", b)
	}
	if cfgs, _, _ := ops.snapshot(); len(cfgs) != 1 {
		t.Errorf("an unknown app cost %d sessions, want 1", len(cfgs))
	}
}

// A session that never reached Steam answers every app with an error and
// hands back its output, and is not retried.
func TestAppBuilds_SteamUnreachable(t *testing.T) {
	d, ops := newAppInfoRuntime(t, []string{"Loading Steam API...\x1b[0mOK", "Connecting anonymously to Steam Public...FAILED (No Connection)"})
	resp, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010", "896660")})
	if err != nil {
		t.Fatalf("a session that ran is a response, not an RPC error: %v", err)
	}
	d.appInfo.wg.Wait()
	for _, b := range resp.GetBuilds() {
		if b.GetBuildId() != "" || !strings.Contains(b.GetError(), "no app info at all") {
			t.Errorf("build = %v", b)
		}
	}
	if !strings.Contains(resp.GetRawTail(), "FAILED (No Connection)") {
		t.Errorf("raw_tail should carry the login failure: %q", resp.GetRawTail())
	}
	if cfgs, _, _ := ops.snapshot(); len(cfgs) != 1 {
		t.Errorf("an unreachable Steam cost %d sessions, want 1", len(cfgs))
	}
}

func TestAppBuilds_RefusesBadRequests(t *testing.T) {
	d, ops := newAppInfoRuntime(t)
	ctx := deadlineCtx(t, time.Minute)
	for name, req := range map[string]*agentpb.GetAppBuildsRequest{
		"no image":             {Apps: queries("2394010")},
		"app id carries shell": {Image: testRef, Apps: queries("2394010; rm -rf /")},
		"empty app id":         {Image: testRef, Apps: queries("")},
	} {
		if _, err := d.AppBuilds(ctx, req); grpcstatus.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: want InvalidArgument, got %v", name, err)
		}
	}
	if _, err := d.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{Image: testRef, PlatformType: "windows", Apps: queries("2394010")}); grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Errorf("a windows check on a linux daemon: want FailedPrecondition, got %v", err)
	}
	if resp, err := d.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{Image: testRef}); err != nil || len(resp.GetBuilds()) != 0 {
		t.Errorf("no apps: want an empty answer, got %v, %v", resp, err)
	}
	if cfgs, _, _ := ops.snapshot(); len(cfgs) != 0 {
		t.Errorf("a refused request ran %d sessions", len(cfgs))
	}
}

// A fleet check and an operator's "Check now" for the same apps share one
// session, whatever order or branches they ask in; a check of a different app
// set waits for the shared container name instead of colliding with it.
func TestAppBuilds_CoalescesAndSerializes(t *testing.T) {
	pal := append([]string{appInfoPreamble}, appBlock("2394010", branchesBody("25247047"))...)
	both := append(append([]string{appInfoPreamble}, appBlock("2394010", branchesBody("25247047"))...), appBlock("896660", branchesBody("25730807"))...)
	d, ops := newAppInfoRuntime(t, both, pal)
	ops.gate = make(chan struct{})
	ops.entered = make(chan struct{}, 4)

	ctx := deadlineCtx(t, time.Minute)
	type result struct {
		resp *agentpb.GetAppBuildsResponse
		err  error
	}
	ask := func(apps ...string) chan result {
		ch := make(chan result, 1)
		go func() {
			r, err := d.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries(apps...)})
			ch <- result{r, err}
		}()
		return ch
	}

	a := ask("2394010", "896660")
	<-ops.entered // the first session is running, its logs held open

	// The same set, asked in another order, with a branch and a duplicate,
	// keys to the same flight.
	ids, err := appInfoIDs(queries("896660@public", "2394010", "2394010"))
	if err != nil || !slices.Equal(ids, []string{"2394010", "896660"}) {
		t.Fatalf("appInfoIDs = %v, %v", ids, err)
	}
	f1 := d.joinAppInfoCheck(testRef, false, ids)
	c := ask("2394010") // a different set: a flight of its own
	waitFor(t, "the second app set's flight", func() bool {
		d.appInfo.mu.Lock()
		defer d.appInfo.mu.Unlock()
		return len(d.appInfo.inflight) == 2
	})
	if f2 := d.joinAppInfoCheck(testRef, false, []string{"2394010", "896660"}); f2 != f1 {
		t.Error("an identical request started a second flight")
	}
	if cfgs, _, _ := ops.snapshot(); len(cfgs) != 1 {
		t.Fatalf("the second app set's session must wait for the first: %d sessions running", len(cfgs))
	}
	close(ops.gate)

	<-f1.done
	for _, id := range ids {
		if a := f1.apps[id]; a.Branches["public"].BuildID == "" {
			t.Errorf("the joined flight has no build for %s: %+v", id, a)
		}
	}
	for name, ch := range map[string]chan result{"a": a, "c": c} {
		r := <-ch
		if r.err != nil {
			t.Fatalf("%s: %v", name, r.err)
		}
		for _, bld := range r.resp.GetBuilds() {
			if bld.GetError() != "" || bld.GetBuildId() == "" {
				t.Errorf("%s: %v", name, bld)
			}
		}
	}
	d.appInfo.wg.Wait()
	_, _, events := ops.snapshot()
	want := []string{"create " + appInfoContainerName, "remove install-1", "create " + appInfoContainerName, "remove install-2"}
	if !slices.Equal(events, want) {
		t.Errorf("events = %v, want two sessions, one after the other", events)
	}
}

// A waiting caller whose context ends stops waiting; the check it joined
// carries on for anyone else.
func TestAppBuilds_CallerGivesUp(t *testing.T) {
	// Twice: the second ask may join the first check or, if that has already
	// finished, start its own.
	session := append([]string{appInfoPreamble}, appBlock("2394010", branchesBody("25247047"))...)
	d, ops := newAppInfoRuntime(t, session, session)
	ops.gate = make(chan struct{})
	ops.entered = make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := d.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010")})
		errc <- err
	}()
	<-ops.entered
	cancel()
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("want the caller's cancellation, got %v", err)
	}
	close(ops.gate)
	resp, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010")})
	if err != nil || resp.GetBuilds()[0].GetBuildId() != "25247047" {
		t.Errorf("after a caller gave up: %v, %v", resp, err)
	}
}

// A kraken_appinfo left by an earlier check (a removal that did not land, an
// Agent that crashed mid-check) is cleared first; a container under that name
// the Agent did not leave is not touched.
func TestAppBuilds_ClearsLeftoverButNotStranger(t *testing.T) {
	session := append([]string{appInfoPreamble}, appBlock("2394010", branchesBody("25247047"))...)

	d, ops := newAppInfoRuntime(t, session)
	ops.names = map[string]container.InspectResponse{appInfoContainerName: {
		ContainerJSONBase: &container.ContainerJSONBase{ID: exitedID, Name: "/" + appInfoContainerName, State: &container.State{Status: container.StateExited}},
		Config:            &container.Config{Labels: map[string]string{labelManaged: "true"}},
	}}
	if _, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010")}); err != nil {
		t.Fatal(err)
	}
	d.appInfo.wg.Wait()
	if _, _, events := ops.snapshot(); len(events) < 2 || events[0] != "remove "+exitedID || events[1] != "create "+appInfoContainerName {
		t.Errorf("events = %v, want the leftover removed before the create", events)
	}

	d, ops = newAppInfoRuntime(t, session)
	withName(ops.fakeOps, otherID, appInfoContainerName) // no labels: not ours
	if _, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010")}); err == nil || !strings.Contains(err.Error(), "refusing to remove it") {
		t.Errorf("want a refusal, got %v", err)
	}
	d.appInfo.wg.Wait()
	if _, _, events := ops.snapshot(); len(events) != 0 {
		t.Errorf("a stranger's container was touched: %v", events)
	}
}

// The image is made available by the normal pull policy first; a node that
// cannot have it fails the check as a whole.
func TestAppBuilds_MissingImageFailsTheCheck(t *testing.T) {
	d, ops := newAppInfoRuntime(t)
	d.images = &fakeImages{}
	_, err := d.AppBuilds(deadlineCtx(t, time.Minute), &agentpb.GetAppBuildsRequest{Image: testRef, Apps: queries("2394010")})
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "not present on this node") {
		t.Errorf("want FailedPrecondition naming the missing image, got %v", err)
	}
	if cfgs, _, _ := ops.snapshot(); len(cfgs) != 0 {
		t.Errorf("ran %d sessions without an image", len(cfgs))
	}
}

// Windows: steamcmd.exe, through the same self-update guard as an install,
// since the relaunch-and-exit trap belongs to the binary.
func TestAppInfoScript(t *testing.T) {
	linux, guarded := appInfoScript(false, []string{"2394010", "896660"})
	if guarded || linux != "steamcmd +login anonymous +app_info_update 1 +app_info_print 2394010 +app_info_print 896660 +quit" {
		t.Errorf("linux = %q guarded=%v", linux, guarded)
	}
	win, guarded := appInfoScript(true, []string{"2394010"})
	if !guarded {
		t.Fatal("the windows check must go through the self-update guard")
	}
	for _, want := range []string{
		steamcmdPrime + " & " + steamcmdWaitCmd + " & ",
		"steamcmd.exe +login anonymous +app_info_update 1 +app_info_print 2394010 +quit & " + steamcmdWaitCmd,
	} {
		if !strings.Contains(win, want) {
			t.Errorf("windows script %q lacks %q", win, want)
		}
	}
}

// The check's container is managed but belongs to no server, so NodeInfo's
// roll call leaves it out rather than report a game container with no server.
func TestReportManagedContainersOmitsTheBuildCheck(t *testing.T) {
	ops := &listingOps{containers: []container.Summary{
		managedSummary("srv-a", "kraken_srv-a", "running"),
		managedSummary("", appInfoContainerName, "running"),
	}}
	list, live, _ := reportManagedContainers(context.Background(), ops)
	if live != 1 || len(list) != 1 || list[0].GetServerId() != "srv-a" {
		t.Errorf("list = %v live = %d, want only srv-a", list, live)
	}
}
