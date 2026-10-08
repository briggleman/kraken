package agent

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The fake's half of the Steam build check (#392): the seeded table answers
// GetAppBuilds, an install writes the current build into the server's
// manifest, and a later bump leaves that manifest behind — the shape every
// Panel-side test of the check needs.
func TestFakeAppBuilds_TableManifestAndBump(t *testing.T) {
	f := NewFakeRuntime("n1", "linux", false, "test",
		WithFakeAppBuilds(map[string]FakeAppBuild{"2394010": {BuildID: "100", TimeUpdated: 1700000000}}))
	ctx := context.Background()

	resp, err := f.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{Apps: []*agentpb.AppBuildQuery{
		{AppId: "2394010", Branch: "public"},
		{AppId: "999", Branch: "public"},
		{AppId: "2394010", Branch: "beta"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Builds[0]; got.BuildId != "100" || got.TimeUpdated != 1700000000 || got.Error != "" {
		t.Errorf("known app: %+v", got)
	}
	if got := resp.Builds[1]; got.BuildId != "" || got.Error == "" {
		t.Errorf("unknown app should carry a per-app error: %+v", got)
	}
	if got := resp.Builds[2]; got.Error == "" {
		t.Errorf("unknown branch should carry a per-app error: %+v", got)
	}

	// An install stamps the manifest with the build of the day.
	if err := f.Create(ctx, &agentpb.ServerSpec{ServerId: "sv-1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Install(ctx, &agentpb.InstallServerRequest{ServerId: "sv-1", InstallScript: "steamcmd", Env: map[string]string{"APP_ID": "2394010"}},
		func(*agentpb.InstallEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	data, _, _, _, err := f.ReadFile(ctx, "sv-1", "steamapps/appmanifest_2394010.acf", 1<<20)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if !strings.Contains(string(data), "\"buildid\"\t\t\"100\"") {
		t.Errorf("manifest does not carry the installed build:\n%s", data)
	}

	// Steam ships a new build: the table moves, the manifest does not.
	f.SetAppBuild("2394010", FakeAppBuild{BuildID: "101"})
	resp, _ = f.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{Apps: []*agentpb.AppBuildQuery{{AppId: "2394010"}}})
	if resp.Builds[0].BuildId != "101" {
		t.Errorf("after the bump: %+v", resp.Builds[0])
	}
	data, _, _, _, _ = f.ReadFile(ctx, "sv-1", "steamapps/appmanifest_2394010.acf", 1<<20)
	if !strings.Contains(string(data), "\"buildid\"\t\t\"100\"") {
		t.Errorf("the manifest should still say 100 until the next install:\n%s", data)
	}

	// An Agent that predates the RPC.
	f.SetAppBuildsError(grpcstatus.Error(codes.Unimplemented, "old agent"))
	if _, err := f.AppBuilds(ctx, &agentpb.GetAppBuildsRequest{}); grpcstatus.Code(err) != codes.Unimplemented {
		t.Errorf("want Unimplemented, got %v", err)
	}
}
