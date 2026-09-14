package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// fakeImages is the imageAPI seam: enough of the Docker client to drive the
// pull/fallback policy without a daemon (#288). It is mutex-guarded because the
// start path detaches pulls into their own goroutine (see refreshImageForStart),
// so a test and a background pull touch it at the same time.
type fakeImages struct {
	mu      sync.Mutex
	local   map[string]image.InspectResponse
	pullErr error
	pulls   []string
	// pulled, when set, is added to local once a pull succeeds, so the
	// post-pull re-inspect sees what the registry delivered.
	pulled *image.InspectResponse
	// gate, when set, blocks every ImagePull until the test closes it — this is
	// how a "still downloading" pull is simulated.
	gate chan struct{}
	// pullCtxErr is the state of the pull's context when the pull finished,
	// recorded to prove a detached pull outlives the RPC that started it.
	pullCtxErr error
}

func (f *fakeImages) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info, ok := f.local[ref]; ok {
		return info, nil
	}
	return image.InspectResponse{}, errors.New("no such image: " + ref)
}

func (f *fakeImages) ImagePull(ctx context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	f.pulls = append(f.pulls, ref)
	gate := f.gate
	f.mu.Unlock()

	if gate != nil {
		<-gate
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullCtxErr = ctx.Err()
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	if f.pulled != nil {
		if f.local == nil {
			f.local = map[string]image.InspectResponse{}
		}
		f.local[ref] = *f.pulled
	}
	return io.NopCloser(strings.NewReader(`{"status":"Downloaded"}`)), nil
}

// pullCount is the number of ImagePull calls so far.
func (f *fakeImages) pullCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pulls)
}

func newPullRuntime(t *testing.T, policy imagePullPolicy, f *fakeImages) *DockerRuntime {
	t.Helper()
	return &DockerRuntime{images: f, pullPolicy: policy}
}

// collectLog returns a log sink plus a func that joins everything written to it.
func collectLog() (func(string), func() string) {
	var lines []string
	return func(s string) { lines = append(lines, s) }, func() string { return strings.Join(lines, "\n") }
}

const testRef = "ghcr.io/briggleman/kraken-steam-base:latest"

func TestPullImagePullsEvenWhenPresentLocally(t *testing.T) {
	// The bug in #288: a locally-present image short-circuited the pull, so an
	// image fix never reached the node.
	f := &fakeImages{
		local:  map[string]image.InspectResponse{testRef: {ID: "sha256:" + strings.Repeat("a", 64)}},
		pulled: &image.InspectResponse{ID: "sha256:" + strings.Repeat("b", 64), RepoDigests: []string{testRef + "@sha256:" + strings.Repeat("c", 64)}},
	}
	d := newPullRuntime(t, pullAlways, f)
	log, dump := collectLog()
	if err := d.pullImage(context.Background(), testRef, time.Minute, log); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if f.pullCount() != 1 {
		t.Fatalf("expected exactly one pull, got %d", f.pullCount())
	}
	// The identity of the image the node ended up on must be in the log, so
	// "which image is this node on?" is answerable from the install log (#284).
	if got := dump(); !strings.Contains(got, "Image ready") || !strings.Contains(got, strings.Repeat("c", 12)) {
		t.Errorf("log does not report the pulled image's identity:\n%s", got)
	}
}

func TestPullImageFallsBackToLocalOnPullFailure(t *testing.T) {
	f := &fakeImages{
		local:   map[string]image.InspectResponse{testRef: {ID: "sha256:" + strings.Repeat("d", 64)}},
		pullErr: errors.New("dial tcp: connection refused"),
	}
	d := newPullRuntime(t, pullAlways, f)
	log, dump := collectLog()
	if err := d.pullImage(context.Background(), testRef, time.Minute, log); err != nil {
		t.Fatalf("a registry failure must not fail an install that has a local image: %v", err)
	}
	got := dump()
	if !strings.Contains(got, "Pull of") || !strings.Contains(got, "using local image") {
		t.Errorf("the fallback must be logged, not silent:\n%s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("the fallback must log why the pull failed:\n%s", got)
	}
}

func TestPullImageFailsWhenPullFailsAndNothingLocal(t *testing.T) {
	f := &fakeImages{pullErr: errors.New("manifest unknown")}
	d := newPullRuntime(t, pullAlways, f)
	log, _ := collectLog()
	err := d.pullImage(context.Background(), testRef, time.Minute, log)
	if err == nil {
		t.Fatal("expected an error when neither the registry nor the node has the image")
	}
	if !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("error should carry the registry's reason, got %v", err)
	}
}

func TestPullImageSkipsDigestPinnedRefWithLocalHit(t *testing.T) {
	ref := "ghcr.io/briggleman/kraken-steam-base@sha256:" + strings.Repeat("e", 64)
	f := &fakeImages{local: map[string]image.InspectResponse{ref: {ID: "sha256:" + strings.Repeat("f", 64)}}}
	d := newPullRuntime(t, pullAlways, f)
	log, dump := collectLog()
	if err := d.pullImage(context.Background(), ref, time.Minute, log); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if f.pullCount() != 0 {
		t.Errorf("a digest ref is immutable — it must not be re-pulled, got %d", f.pullCount())
	}
	if got := dump(); !strings.Contains(got, "immutable") {
		t.Errorf("the skip reason should be logged:\n%s", got)
	}
}

func TestPullImageDigestRefStillPullsWhenAbsent(t *testing.T) {
	ref := "ghcr.io/briggleman/kraken-steam-base@sha256:" + strings.Repeat("e", 64)
	f := &fakeImages{}
	d := newPullRuntime(t, pullAlways, f)
	log, _ := collectLog()
	if err := d.pullImage(context.Background(), ref, time.Minute, log); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if f.pullCount() != 1 {
		t.Errorf("a digest ref that is not on the node must be fetched, got %d", f.pullCount())
	}
}

func TestPullImagePolicyIfNotPresent(t *testing.T) {
	f := &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: "sha256:" + strings.Repeat("a", 64)}}}
	d := newPullRuntime(t, pullIfNotPresent, f)
	log, dump := collectLog()
	if err := d.pullImage(context.Background(), testRef, time.Minute, log); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if f.pullCount() != 0 {
		t.Errorf(`"if-not-present" must not contact the registry for an image already on the node, got %d`, f.pullCount())
	}
	if got := dump(); !strings.Contains(got, "if-not-present") {
		t.Errorf("the policy decision should be logged:\n%s", got)
	}

	// …but it is still the thing that fetches a missing image.
	missing := &fakeImages{}
	d2 := newPullRuntime(t, pullIfNotPresent, missing)
	if err := d2.pullImage(context.Background(), testRef, time.Minute, func(string) {}); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if missing.pullCount() != 1 {
		t.Errorf(`"if-not-present" must fetch an image that is not on the node, got %d`, missing.pullCount())
	}
}

func TestPullImagePolicyNever(t *testing.T) {
	f := &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: "sha256:" + strings.Repeat("a", 64)}}}
	d := newPullRuntime(t, pullNever, f)
	if err := d.pullImage(context.Background(), testRef, time.Minute, func(string) {}); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if f.pullCount() != 0 {
		t.Errorf(`"never" must not contact the registry at all, got %d`, f.pullCount())
	}

	// With nothing on the node, "never" fails immediately rather than hanging on
	// a registry the operator has deliberately cut off.
	empty := &fakeImages{}
	d2 := newPullRuntime(t, pullNever, empty)
	err := d2.pullImage(context.Background(), testRef, time.Minute, func(string) {})
	if err == nil {
		t.Fatal(`expected "never" with no local image to fail`)
	}
	if empty.pullCount() != 0 {
		t.Errorf(`"never" must not pull even when the image is missing, got %d`, empty.pullCount())
	}
}

func TestPullImagePropagatesCancellation(t *testing.T) {
	// An operator cancelling an install must not be reported as a registry
	// failure that quietly fell back to the local image.
	f := &fakeImages{
		local:   map[string]image.InspectResponse{testRef: {ID: "sha256:" + strings.Repeat("a", 64)}},
		pullErr: context.Canceled,
	}
	d := newPullRuntime(t, pullAlways, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.pullImage(ctx, testRef, time.Minute, func(string) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancellation to surface, got %v", err)
	}
}

func TestParsePullPolicy(t *testing.T) {
	for in, want := range map[string]imagePullPolicy{
		"":                pullAlways,
		"always":          pullAlways,
		"if-not-present":  pullIfNotPresent,
		" IF-Not-Present": pullIfNotPresent,
		"never":           pullNever,
		"nonsense":        pullAlways,
	} {
		if got := parsePullPolicy(in); got != want {
			t.Errorf("parsePullPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImageIdentityPrefersRepoDigest(t *testing.T) {
	info := image.InspectResponse{
		ID:          "sha256:" + strings.Repeat("a", 64),
		RepoDigests: []string{testRef + "@sha256:" + strings.Repeat("b", 64)},
	}
	if got := imageIdentity(info); got != strings.Repeat("b", 12) {
		t.Errorf("imageIdentity = %q, want the repo digest", got)
	}
	if got := imageIdentity(image.InspectResponse{ID: "sha256:" + strings.Repeat("a", 64)}); got != strings.Repeat("a", 12) {
		t.Errorf("imageIdentity = %q, want the image ID", got)
	}
	if got := imageIdentity(image.InspectResponse{}); got != "unknown" {
		t.Errorf("imageIdentity = %q, want %q", got, "unknown")
	}
}
