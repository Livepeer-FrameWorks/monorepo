package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeEdgeReleaseQM is a hand-written stand-in for the Quartermaster release
// surface. It records the requests it receives and returns canned responses,
// so the seam-extracted run* handlers are tested without a live control plane.
type fakeEdgeReleaseQM struct {
	listResp *quartermasterpb.ListEdgeReleasesResponse
	listErr  error
	getResp  *quartermasterpb.ClusterReleaseTargetResponse
	getErr   error
	setResp  *quartermasterpb.ClusterReleaseTargetResponse
	setErr   error

	gotGet *quartermasterpb.GetClusterReleaseTargetRequest
	gotSet *quartermasterpb.SetClusterReleaseTargetRequest
}

func (f *fakeEdgeReleaseQM) ListEdgeReleases(_ context.Context, _ *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeEdgeReleaseQM) GetClusterReleaseTarget(_ context.Context, req *quartermasterpb.GetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	f.gotGet = req
	return f.getResp, f.getErr
}

func (f *fakeEdgeReleaseQM) SetClusterReleaseTarget(_ context.Context, req *quartermasterpb.SetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	f.gotSet = req
	return f.setResp, f.setErr
}

func TestRunReleasesListEmpty(t *testing.T) {
	var buf bytes.Buffer
	qm := &fakeEdgeReleaseQM{listResp: &quartermasterpb.ListEdgeReleasesResponse{}}
	if err := runReleasesList(context.Background(), &buf, qm, "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "No edge releases found.") {
		t.Fatalf("expected empty notice, got %q", buf.String())
	}
}

func TestRunReleasesListRenders(t *testing.T) {
	var buf bytes.Buffer
	qm := &fakeEdgeReleaseQM{listResp: &quartermasterpb.ListEdgeReleasesResponse{
		Releases: []*quartermasterpb.EdgeRelease{
			nil, // nil entries are skipped, not fatal
			{Channel: "stable", Version: "v1.0.0", ComponentsJson: `{"helmsman":{}}`},
			{Channel: "rc", Version: "v1.1.0-rc1"},
		},
	}}
	if err := runReleasesList(context.Background(), &buf, qm, "stable", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"stable/v1.0.0", "rc/v1.1.0-rc1", "published=-", `components={"helmsman":{}}`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunReleasesListError(t *testing.T) {
	qm := &fakeEdgeReleaseQM{listErr: errors.New("rpc boom")}
	if err := runReleasesList(context.Background(), &bytes.Buffer{}, qm, "", ""); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRunReleaseTargetGet(t *testing.T) {
	var buf bytes.Buffer
	qm := &fakeEdgeReleaseQM{getResp: &quartermasterpb.ClusterReleaseTargetResponse{
		Target: &quartermasterpb.ClusterReleaseTarget{
			ClusterId: "cluster-1", Channel: "stable", TargetVersion: "", Paused: true, RolloutPlanJson: "{}",
		},
	}}
	if err := runReleaseTargetGet(context.Background(), &buf, qm, "cluster-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// Empty target version renders as "latest".
	for _, want := range []string{"cluster=cluster-1", "track=stable", "version=latest", "paused=true", "rollout_plan={}"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if qm.gotGet.GetClusterId() != "cluster-1" {
		t.Errorf("request cluster id = %q", qm.gotGet.GetClusterId())
	}
}

func TestRunReleaseTargetSet(t *testing.T) {
	var buf bytes.Buffer
	qm := &fakeEdgeReleaseQM{setResp: &quartermasterpb.ClusterReleaseTargetResponse{
		Target: &quartermasterpb.ClusterReleaseTarget{ClusterId: "c2", Channel: "rc", TargetVersion: "v2.0.0", Paused: false},
	}}
	target := &quartermasterpb.ClusterReleaseTarget{ClusterId: "c2", Channel: "rc", TargetVersion: "v2.0.0", RolloutPlanJson: "{}"}
	if err := runReleaseTargetSet(context.Background(), &buf, qm, target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"release-target", "cluster=c2", "track=rc", "version=v2.0.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	// The handler must forward the caller-built target unchanged.
	if qm.gotSet.GetTarget().GetClusterId() != "c2" || qm.gotSet.GetTarget().GetChannel() != "rc" {
		t.Errorf("forwarded target mismatch: %+v", qm.gotSet.GetTarget())
	}
}

func TestRunReleaseTargetSetError(t *testing.T) {
	qm := &fakeEdgeReleaseQM{setErr: errors.New("denied")}
	err := runReleaseTargetSet(context.Background(), &bytes.Buffer{}, qm, &quartermasterpb.ClusterReleaseTarget{})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

// existingReleaseTargetControls treats a NotFound target as "no override yet"
// (default rollout plan, unpaused) so a first-time sync does not error.
func TestExistingReleaseTargetControls(t *testing.T) {
	t.Run("not found defaults to empty plan", func(t *testing.T) {
		qm := &fakeEdgeReleaseQM{getErr: status.Error(codes.NotFound, "missing")}
		plan, paused, err := existingReleaseTargetControls(context.Background(), qm, "c1")
		if err != nil {
			t.Fatalf("NotFound should not error: %v", err)
		}
		if plan != "{}" || paused {
			t.Fatalf("got plan=%q paused=%v, want {} false", plan, paused)
		}
	})

	t.Run("existing target controls are returned", func(t *testing.T) {
		qm := &fakeEdgeReleaseQM{getResp: &quartermasterpb.ClusterReleaseTargetResponse{
			Target: &quartermasterpb.ClusterReleaseTarget{RolloutPlanJson: `{"step":1}`, Paused: true},
		}}
		plan, paused, err := existingReleaseTargetControls(context.Background(), qm, "c1")
		if err != nil || plan != `{"step":1}` || !paused {
			t.Fatalf("got plan=%q paused=%v err=%v", plan, paused, err)
		}
	})

	t.Run("nil target defaults to empty plan", func(t *testing.T) {
		qm := &fakeEdgeReleaseQM{getResp: &quartermasterpb.ClusterReleaseTargetResponse{}}
		plan, paused, err := existingReleaseTargetControls(context.Background(), qm, "c1")
		if err != nil || plan != "{}" || paused {
			t.Fatalf("got plan=%q paused=%v err=%v", plan, paused, err)
		}
	})

	t.Run("non-NotFound error propagates", func(t *testing.T) {
		qm := &fakeEdgeReleaseQM{getErr: status.Error(codes.Unavailable, "down")}
		if _, _, err := existingReleaseTargetControls(context.Background(), qm, "c1"); err == nil {
			t.Fatal("expected non-NotFound error to propagate")
		}
	})
}

// --- pure helpers (untested before this pass; checksum/component/selector and
// platformKey/Name helpers are already covered in coverage_batch2_test.go) ---

func TestShouldPublishReleaseForTarget(t *testing.T) {
	platformWithToken := fwcfg.Context{Persona: fwcfg.PersonaPlatform, Auth: fwcfg.Auth{ServiceToken: "tok"}}
	if !shouldPublishReleaseForTarget(platformWithToken) {
		t.Error("platform persona with service token should publish")
	}
	if shouldPublishReleaseForTarget(fwcfg.Context{Persona: fwcfg.PersonaPlatform}) {
		t.Error("platform persona without token should not publish")
	}
	if shouldPublishReleaseForTarget(fwcfg.Context{Persona: fwcfg.PersonaUser, Auth: fwcfg.Auth{ServiceToken: "tok"}}) {
		t.Error("non-platform persona should not publish")
	}
}

// platformFilterSet gates whether an explicit --os/--arch filter was supplied.
func TestPlatformFilterSet(t *testing.T) {
	if platformFilterSet("", "") {
		t.Error("empty os/arch should not be a filter")
	}
	if !platformFilterSet("linux", "") || !platformFilterSet("", "amd64") {
		t.Error("any non-empty os/arch is a filter")
	}
	if !platformFilterSet(" linux ", "") {
		t.Error("whitespace-padded value is still a filter")
	}
}
