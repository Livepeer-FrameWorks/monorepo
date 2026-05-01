package datamigrate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// trivialSemver compares vMAJ.MIN.PAT lexicographically (after a length pad)
// — only used in tests; the real catalog supplies releases.CompareSemver.
func trivialSemver(a, b string) int {
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

func staticSource(t *testing.T, statuses map[string]LiveStatus) StateSource {
	t.Helper()
	return func(_ context.Context, service, id string) LiveStatus {
		if got, ok := statuses[service+"."+id]; ok {
			return got
		}
		return LiveStatus{ID: id, Service: service, NotAdopted: true}
	}
}

func TestPreDeployBlockers_EmptyRequirements(t *testing.T) {
	got, err := PreDeployBlockers(context.Background(), staticSource(t, nil), nil, "v0.4.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty reqs must yield zero blockers; got %d", len(got))
	}
}

func TestPreDeployBlockers_PriorPendingBlocks(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.4.0", RequiredBeforeVersion: "v0.5.0", RequiredBeforePhase: "postdeploy"},
	}
	src := staticSource(t, map[string]LiveStatus{
		"purser.m1": {ID: "m1", Service: "purser", Status: StatusPending},
	})
	got, err := PreDeployBlockers(context.Background(), src, reqs, "v0.3.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d blockers, want 1", len(got))
	}
	if !strings.Contains(got[0].Reason, "pending") {
		t.Errorf("reason should mention pending: %v", got[0].Reason)
	}
}

func TestPreDeployBlockers_PriorCompletedPasses(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.4.0", RequiredBeforeVersion: "v0.5.0"},
	}
	src := staticSource(t, map[string]LiveStatus{
		"purser.m1": {ID: "m1", Service: "purser", Status: StatusCompleted},
	})
	got, err := PreDeployBlockers(context.Background(), src, reqs, "v0.3.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("completed prior must not block; got %d blockers", len(got))
	}
}

func TestPreDeployBlockers_NotAdoptedBlocks(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.4.0", RequiredBeforeVersion: "v0.5.0"},
	}
	// staticSource fallback returns NotAdopted for unknown ids.
	got, err := PreDeployBlockers(context.Background(), staticSource(t, nil), reqs, "v0.3.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Reason, "not adopted") {
		t.Errorf("expected NotAdopted blocker, got %+v", got)
	}
}

func TestPreDeployBlockers_NotRegisteredBlocks(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.4.0", RequiredBeforeVersion: "v0.5.0"},
	}
	src := staticSource(t, map[string]LiveStatus{
		"purser.m1": {ID: "m1", Service: "purser", NotRegistered: true},
	})
	got, err := PreDeployBlockers(context.Background(), src, reqs, "v0.3.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Reason, "not registered") {
		t.Errorf("expected NotRegistered blocker, got %+v", got)
	}
}

func TestPreDeployBlockers_FetchErrorBlocks(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.4.0", RequiredBeforeVersion: "v0.5.0"},
	}
	src := staticSource(t, map[string]LiveStatus{
		"purser.m1": {ID: "m1", Service: "purser", FetchError: errors.New("ssh died")},
	})
	got, err := PreDeployBlockers(context.Background(), src, reqs, "v0.3.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Reason, "ssh died") {
		t.Errorf("expected fetch-error blocker, got %+v", got)
	}
}

func TestPreDeployBlockers_TargetMigrationsExcluded(t *testing.T) {
	// A migration introduced IN the target version is the postdeploy gate's
	// concern, not the pre-deploy gate's.
	reqs := []Requirement{
		{ID: "target", Service: "purser", IntroducedIn: "v0.5.0"},
	}
	got, err := PreDeployBlockers(context.Background(), staticSource(t, nil), reqs, "v0.4.0", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("target-version migrations must not appear in pre-deploy; got %d", len(got))
	}
}

func TestPrePostdeployBlockers_TargetPendingBlocks(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.5.0", RequiredBeforePhase: "postdeploy"},
	}
	src := staticSource(t, map[string]LiveStatus{
		"purser.m1": {ID: "m1", Service: "purser", Status: StatusPending},
	})
	got, err := PrePostdeployBlockers(context.Background(), src, reqs, "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d blockers, want 1", len(got))
	}
}

func TestPrePostdeployBlockers_NonPostdeployIgnored(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.5.0", RequiredBeforePhase: "contract"},
	}
	got, err := PrePostdeployBlockers(context.Background(), staticSource(t, nil), reqs, "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("non-postdeploy requirements must not appear; got %d", len(got))
	}
}

func TestPrePhaseBlockers_ContractPendingBlocks(t *testing.T) {
	reqs := []Requirement{
		{ID: "m1", Service: "purser", IntroducedIn: "v0.5.0", RequiredBeforePhase: "contract"},
	}
	src := staticSource(t, map[string]LiveStatus{
		"purser.m1": {ID: "m1", Service: "purser", Status: StatusPending},
	})
	got, err := PrePhaseBlockers(context.Background(), src, reqs, "contract", "v0.5.0", trivialSemver)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d blockers, want 1", len(got))
	}
}

func TestBlockersRejectNilSource(t *testing.T) {
	if _, err := PreDeployBlockers(context.Background(), nil, nil, "", "", trivialSemver); err == nil {
		t.Error("nil StateSource must error")
	}
	if _, err := PrePostdeployBlockers(context.Background(), nil, nil, "", trivialSemver); err == nil {
		t.Error("nil StateSource must error")
	}
	if _, err := PrePhaseBlockers(context.Background(), nil, nil, "contract", "", trivialSemver); err == nil {
		t.Error("nil StateSource must error")
	}
}

func TestBlockersRejectNilCompare(t *testing.T) {
	src := staticSource(t, nil)
	if _, err := PreDeployBlockers(context.Background(), src, nil, "", "", nil); err == nil {
		t.Error("nil semverCompare must error")
	}
	if _, err := PrePostdeployBlockers(context.Background(), src, nil, "", nil); err == nil {
		t.Error("nil semverCompare must error")
	}
	if _, err := PrePhaseBlockers(context.Background(), src, nil, "contract", "", nil); err == nil {
		t.Error("nil semverCompare must error")
	}
}
