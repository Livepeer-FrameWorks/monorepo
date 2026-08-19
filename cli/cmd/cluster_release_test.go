package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	fwv "github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

// TestValidateFetchedReleaseCompatibility covers the fail-closed gate driven by FETCHED release metadata: an outdated
// CLI (below min_cli_version) or one missing a required transition must be refused BEFORE any migration.
func TestValidateFetchedReleaseCompatibility(t *testing.T) {
	// A required transition this CLI does not implement → fail closed.
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v9.9.9", RequiredTransitions: []string{"transition-from-the-future"}}, false); err == nil || !strings.Contains(err.Error(), "does not implement") {
		t.Fatalf("unknown transition must fail closed, got: %v", err)
	}

	old := fwv.Version
	t.Cleanup(func() { fwv.Version = old })

	// min_cli_version above a concrete CLI build → fail closed.
	fwv.Version = "v0.2.0"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.3.0", MinCLIVersion: "v0.3.0"}, false); err == nil || !strings.Contains(err.Error(), "older than the release's required minimum") {
		t.Fatalf("an old CLI must fail the min-cli floor, got: %v", err)
	}
	fwv.Version = "v0.3.0"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.3.0", MinCLIVersion: "v0.3.0"}, false); err != nil {
		t.Fatalf("a new-enough CLI must pass the floor, got: %v", err)
	}

	// A NON-CONCRETE local build (dev/unversioned) now FAILS CLOSED against the floor — the previous silent "dev is
	// never blocked" exemption is gone, so a locally built CLI cannot ignore a release's required tooling floor.
	fwv.Version = "dev"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.3.0", MinCLIVersion: "v99.0.0"}, false); err == nil || !strings.Contains(err.Error(), "non-concrete") {
		t.Fatalf("a dev build must fail closed against the floor, got: %v", err)
	}
	// The explicit unsafe override (allowUnsafe=true) is the only escape hatch: it passes with a LOUD warning.
	var warn bytes.Buffer
	if err := validateFetchedReleaseCompatibility(&warn, &gitops.Manifest{PlatformVersion: "v0.3.0", MinCLIVersion: "v99.0.0"}, true); err != nil {
		t.Fatalf("the unsafe override must bypass the floor, got: %v", err)
	}
	if !strings.Contains(warn.String(), "BYPASSED") {
		t.Fatalf("the unsafe override must emit a loud warning, got: %q", warn.String())
	}
}

// fakeTransition is a minimal ReleaseTransition for sequencing tests.
type fakeTransition struct {
	id          string
	after       []string
	before      []string
	disposition ProvisionDisposition
}

func (f fakeTransition) ID() string               { return f.id }
func (f fakeTransition) Title() string            { return f.id }
func (f fakeTransition) IntroducedIn() string     { return "v0.0.0" }
func (f fakeTransition) Irreversible() bool       { return false }
func (f fakeTransition) AfterServices() []string  { return f.after }
func (f fakeTransition) BeforeServices() []string { return f.before }
func (f fakeTransition) Scopes(*reconcileEnv) ([]ReconcileScope, error) {
	return nil, nil
}
func (f fakeTransition) Check(context.Context, *reconcileEnv, ReconcileScope) ReconcileCheck {
	return ReconcileCheck{}
}
func (f fakeTransition) Apply(context.Context, *reconcileEnv, ReconcileScope) error  { return nil }
func (f fakeTransition) Verify(context.Context, *reconcileEnv, ReconcileScope) error { return nil }
func (f fakeTransition) ProvisionDisposition() ProvisionDisposition                  { return f.disposition }

func seqLabels(steps []releaseStep) []string {
	var out []string
	for _, s := range steps {
		if s.transition != nil {
			out = append(out, "@"+s.transition.ID())
		} else {
			out = append(out, s.upgradeService)
		}
	}
	return out
}

// identity deploy mapping for tests
func idDeploy(s string) string { return s }

func TestResolvedClusterSystemTenantUsesLifecycleState(t *testing.T) {
	rc := &resolvedCluster{Source: inventory.SourceContext, ContextSystemTenantID: " tenant-from-context "}
	got, err := rc.ResolveSystemTenantID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "tenant-from-context" {
		t.Fatalf("system tenant = %q, want context identity", got)
	}

	// Resolution is stable for the whole release, even if mutable caller state
	// changes between service replicas.
	rc.ContextSystemTenantID = "different"
	again, err := rc.ResolveSystemTenantID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Fatalf("cached system tenant = %q, want %q", again, got)
	}
}

func TestPlanReleaseSequence_TransitionSlotsBetweenDeclaredServices(t *testing.T) {
	tr := fakeTransition{id: "catalog-reconcile", after: []string{"quartermaster"}, before: []string{"foghorn", "chandler"}}
	services := []string{"quartermaster", "commodore", "foghorn", "chandler"}

	steps, err := planReleaseSequence(services, idDeploy, []ReleaseTransition{tr})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := strings.Join(seqLabels(steps), " ")
	// quartermaster upgrades first, then the transition (before foghorn), then foghorn, then chandler. commodore may
	// appear anywhere it is ordered by the planner; here after quartermaster.
	qmIdx := indexOf(got, "quartermaster")
	trIdx := indexOf(got, "@catalog-reconcile")
	fhIdx := indexOf(got, "foghorn")
	chIdx := indexOf(got, "chandler")
	if qmIdx >= trIdx || trIdx >= fhIdx || fhIdx >= chIdx {
		t.Fatalf("expected quartermaster < @catalog-reconcile < foghorn < chandler, got: %s", got)
	}
}

func TestPlanReleaseSequence_QMAlreadyCurrentStillRunsTransition(t *testing.T) {
	// Quartermaster is not in the upgrade list (already current); the transition's AfterService is satisfied and it
	// must still run before foghorn.
	tr := fakeTransition{id: "catalog-reconcile", after: []string{"quartermaster"}, before: []string{"foghorn"}}
	services := []string{"foghorn", "chandler"}

	steps, err := planReleaseSequence(services, idDeploy, []ReleaseTransition{tr})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := strings.Join(seqLabels(steps), " ")
	if indexOf(got, "@catalog-reconcile") > indexOf(got, "foghorn") || indexOf(got, "@catalog-reconcile") < 0 {
		t.Fatalf("transition must run before foghorn even when QM is already current, got: %s", got)
	}
}

func TestPlanReleaseSequence_NoServicesRunsTransitionAtEnd(t *testing.T) {
	// Everything already current: the transition still runs (its prerequisites are trivially satisfied).
	tr := fakeTransition{id: "storage", after: []string{"quartermaster"}, before: []string{"foghorn"}}
	steps, err := planReleaseSequence(nil, idDeploy, []ReleaseTransition{tr})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(steps) != 1 || steps[0].transition == nil || steps[0].transition.ID() != "storage" {
		t.Fatalf("expected the transition to run once at the end, got %v", seqLabels(steps))
	}
}

func TestPlanReleaseSequence_OrderingViolationErrors(t *testing.T) {
	// A transition that must run before quartermaster but after foghorn, with foghorn ordered first, is impossible.
	tr := fakeTransition{id: "bad", after: []string{"foghorn"}, before: []string{"quartermaster"}}
	services := []string{"quartermaster", "foghorn"}
	if _, err := planReleaseSequence(services, idDeploy, []ReleaseTransition{tr}); err == nil {
		t.Fatalf("expected a release-ordering error")
	}
}

func indexOf(haystack, needle string) int {
	return strings.Index(haystack, needle)
}

// TestReleaseTransition_IntroducedInMatchesCatalog binds the two authorities for a transition's introduction: the
// compiled handler's IntroducedIn() and the embedded catalog's introduced_in. Execution and ordering use the catalog,
// so IntroducedIn() is otherwise unread — this test makes the duplicated value fail closed on drift rather than silently
// diverge, and confirms every compiled transition is declared in the catalog.
func TestReleaseTransition_IntroducedInMatchesCatalog(t *testing.T) {
	catalog, err := releases.ReleaseTransitionsUpTo("v999.999.999")
	if err != nil {
		t.Fatalf("read release-transition catalog: %v", err)
	}
	catIntro := map[string]string{}
	for _, rt := range catalog {
		catIntro[rt.ID] = rt.IntroducedIn
	}
	for id, tr := range releaseTransitionRegistry {
		want, ok := catIntro[id]
		if !ok {
			t.Errorf("compiled transition %q is not declared in the release-transition catalog", id)
			continue
		}
		if tr.IntroducedIn() != want {
			t.Errorf("transition %q IntroducedIn()=%q but catalog introduced_in=%q — the two authorities have drifted", id, tr.IntroducedIn(), want)
		}
	}
}

// TestAssertProvisionSatisfiesTransitions pins the generic provision rules for future transitions.
func TestAssertProvisionSatisfiesTransitions(t *testing.T) {
	planned := map[string]bool{"quartermaster": true, "foghorn": true}
	// PHASE-AWARE APPLICABILITY: a transition whose gated services (BeforeServices) are NOT in the plan — e.g.
	// `--only infrastructure`, which schedules no Foghorn/Chandler — is skipped even though it would otherwise be
	// refused. This is the infrastructure-only provision that must be allowed to proceed.
	if err := assertProvisionSatisfiesTransitions("v1.0.0", map[string]bool{}, []ReleaseTransition{fakeTransition{id: "not-applicable", before: []string{"foghorn"}, disposition: ProvisionMustExecute}}); err != nil {
		t.Fatalf("a transition whose gated services are not planned must be skipped (not applicable), got: %v", err)
	}
	// An APPLICABLE transition that must be EXECUTED (ProvisionMustExecute) is refused, routed to release apply.
	if err := assertProvisionSatisfiesTransitions("v1.0.0", planned, []ReleaseTransition{fakeTransition{id: "needs-execution", before: []string{"foghorn"}, disposition: ProvisionMustExecute}}); err == nil {
		t.Fatal("an applicable must-execute transition must be refused on provision (routed to release apply)")
	}
	// An EMPTY boundary must NOT skip the disposition (the safe zero value must still fail closed): a must-execute
	// transition with no BeforeServices is globally applicable and refused, even when the planned set is empty.
	if err := assertProvisionSatisfiesTransitions("v1.0.0", map[string]bool{}, []ReleaseTransition{fakeTransition{id: "empty-boundary-must-execute", disposition: ProvisionMustExecute}}); err == nil {
		t.Fatal("an empty-boundary must-execute transition must not be skipped; it must fail closed")
	}
	// A bootstrap-established transition whose establishing service IS scheduled passes.
	if err := assertProvisionSatisfiesTransitions("v1.0.0", planned, []ReleaseTransition{fakeTransition{id: "bootstrap-ok", before: []string{"foghorn"}, after: []string{"quartermaster"}, disposition: ProvisionEstablishedByBootstrap}}); err != nil {
		t.Fatalf("a bootstrap-established transition with its establishing service planned must pass: %v", err)
	}
	// A bootstrap-established transition with an EMPTY AfterServices proves nothing → refused.
	if err := assertProvisionSatisfiesTransitions("v1.0.0", planned, []ReleaseTransition{fakeTransition{id: "bootstrap-empty", before: []string{"foghorn"}, disposition: ProvisionEstablishedByBootstrap}}); err == nil {
		t.Fatal("a bootstrap-established transition that names no establishing service must be refused")
	}
	// A bootstrap-established transition whose establishing service is NOT scheduled → refused (its bootstrap mechanism
	// will not run), so a mis-declared future transition cannot silently pass an install.
	if err := assertProvisionSatisfiesTransitions("v1.0.0", map[string]bool{"foghorn": true}, []ReleaseTransition{fakeTransition{id: "bootstrap-unplanned", before: []string{"foghorn"}, after: []string{"quartermaster"}, disposition: ProvisionEstablishedByBootstrap}}); err == nil {
		t.Fatal("a bootstrap-established transition whose establishing service is not planned must fail closed")
	}
}
