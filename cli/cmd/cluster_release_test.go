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
	if _, ok := releaseTransitionRegistry["storage-descriptor-adoption"]; !ok {
		t.Fatal("precondition: storage-descriptor-adoption must be a compiled transition")
	}

	// A required transition the compiled registry implements → OK.
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.2.97", RequiredTransitions: []string{"storage-descriptor-adoption"}}, false); err != nil {
		t.Fatalf("known transition must pass, got: %v", err)
	}

	// A required transition this CLI does not implement → fail closed.
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v9.9.9", RequiredTransitions: []string{"transition-from-the-future"}}, false); err == nil || !strings.Contains(err.Error(), "does not implement") {
		t.Fatalf("unknown transition must fail closed, got: %v", err)
	}

	old := fwv.Version
	t.Cleanup(func() { fwv.Version = old })

	// min_cli_version above a concrete CLI build → fail closed.
	fwv.Version = "v0.2.0"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.2.97", MinCLIVersion: "v0.2.50"}, false); err == nil || !strings.Contains(err.Error(), "older than the release's required minimum") {
		t.Fatalf("an old CLI must fail the min-cli floor, got: %v", err)
	}
	// A realistic v0.2.97 manifest declares the transitions the v0.2.97 line requires (matching this CLI's catalog).
	fwv.Version = "v0.3.0"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.2.97", MinCLIVersion: "v0.2.50", RequiredTransitions: []string{"storage-descriptor-adoption"}}, false); err != nil {
		t.Fatalf("a new-enough CLI must pass the floor, got: %v", err)
	}

	// A NON-CONCRETE local build (dev/unversioned) now FAILS CLOSED against the floor — the previous silent "dev is
	// never blocked" exemption is gone, so a locally built CLI cannot ignore a release's required tooling floor.
	fwv.Version = "dev"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.2.97", MinCLIVersion: "v99.0.0", RequiredTransitions: []string{"storage-descriptor-adoption"}}, false); err == nil || !strings.Contains(err.Error(), "non-concrete") {
		t.Fatalf("a dev build must fail closed against the floor, got: %v", err)
	}
	// The explicit unsafe override (allowUnsafe=true) is the only escape hatch: it passes with a LOUD warning.
	var warn bytes.Buffer
	if err := validateFetchedReleaseCompatibility(&warn, &gitops.Manifest{PlatformVersion: "v0.2.97", MinCLIVersion: "v99.0.0", RequiredTransitions: []string{"storage-descriptor-adoption"}}, true); err != nil {
		t.Fatalf("the unsafe override must bypass the floor, got: %v", err)
	}
	if !strings.Contains(warn.String(), "BYPASSED") {
		t.Fatalf("the unsafe override must emit a loud warning, got: %q", warn.String())
	}

	// AUTHORITY BINDING: the fetched required-transition set must match this CLI's catalog for the platform version. A
	// fetched manifest that OMITS a transition the catalog requires (or names an extra one) is refused, so execution
	// cannot silently run a different set than the release declares.
	fwv.Version = "v0.3.0"
	if err := validateFetchedReleaseCompatibility(io.Discard, &gitops.Manifest{PlatformVersion: "v0.2.97"}, false); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("a manifest that omits a catalog-required transition must be refused, got: %v", err)
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

func TestPlanReleaseSequence_StorageAdoptionSlotsBetweenQMAndFoghorn(t *testing.T) {
	tr := fakeTransition{id: "storage", after: []string{"quartermaster"}, before: []string{"foghorn", "chandler"}}
	services := []string{"quartermaster", "commodore", "foghorn", "chandler"}

	steps, err := planReleaseSequence(services, idDeploy, []ReleaseTransition{tr})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := strings.Join(seqLabels(steps), " ")
	// quartermaster upgrades first, then the transition (before foghorn), then foghorn, then chandler. commodore may
	// appear anywhere it is ordered by the planner; here after quartermaster.
	qmIdx := indexOf(got, "quartermaster")
	trIdx := indexOf(got, "@storage")
	fhIdx := indexOf(got, "foghorn")
	chIdx := indexOf(got, "chandler")
	if qmIdx >= trIdx || trIdx >= fhIdx || fhIdx >= chIdx {
		t.Fatalf("expected quartermaster < @storage < foghorn < chandler, got: %s", got)
	}
}

func TestPlanReleaseSequence_QMAlreadyCurrentStillRunsTransition(t *testing.T) {
	// Quartermaster is not in the upgrade list (already current); the transition's AfterService is satisfied and it
	// must still run before foghorn.
	tr := fakeTransition{id: "storage", after: []string{"quartermaster"}, before: []string{"foghorn"}}
	services := []string{"foghorn", "chandler"}

	steps, err := planReleaseSequence(services, idDeploy, []ReleaseTransition{tr})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := strings.Join(seqLabels(steps), " ")
	if indexOf(got, "@storage") > indexOf(got, "foghorn") || indexOf(got, "@storage") < 0 {
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

// selectReleaseTransitions is version-gated by the catalog: the storage transition (introduced v0.2.97) is selected
// for a target at/above it and omitted below it, and the compiled handler must be present (outdated-CLI fail-closed).
func TestSelectReleaseTransitions_VersionGated(t *testing.T) {
	got, err := selectReleaseTransitions("v0.2.97")
	if err != nil {
		t.Fatalf("v0.2.97: %v", err)
	}
	found := false
	for _, tr := range got {
		if tr.ID() == "storage-descriptor-adoption" {
			found = true
		}
	}
	if !found {
		t.Fatalf("storage-descriptor-adoption must be selected at its introduced version")
	}

	// Below the introduced version, it is not required yet.
	older, err := selectReleaseTransitions("v0.2.90")
	if err != nil {
		t.Fatalf("v0.2.90: %v", err)
	}
	for _, tr := range older {
		if tr.ID() == "storage-descriptor-adoption" {
			t.Fatalf("storage-descriptor-adoption must NOT run for a target below its introduced version")
		}
	}
}

// A CANARY of v0.2.97 (v0.2.97-rc1, which sorts BEFORE the final) must still select the storage transition the
// v0.2.97 line introduces — otherwise the rc binary deploys against a schema/authority missing its required work.
func TestSelectReleaseTransitions_CanaryIncludesFinalLine(t *testing.T) {
	got, err := selectReleaseTransitions("v0.2.97-rc1")
	if err != nil {
		t.Fatalf("v0.2.97-rc1: %v", err)
	}
	found := false
	for _, tr := range got {
		if tr.ID() == "storage-descriptor-adoption" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a canary of v0.2.97 must include storage-descriptor-adoption (base v0.2.97 >= introduced v0.2.97)")
	}
}

// TestDirectUpgradeRefusesTransitionGatedService asserts a direct `cluster upgrade` refuses a service
// that sits on the downstream side (BeforeServices) of a required release transition, because only `release apply`
// runs and verifies transitions. For v0.2.97, storage-descriptor-adoption gates foghorn and chandler; a service that
// no required transition gates (purser) is not refused.
func TestDirectUpgradeRefusesTransitionGatedService(t *testing.T) {
	transitions, err := selectReleaseTransitions("v0.2.97")
	if err != nil {
		t.Fatalf("selectReleaseTransitions: %v", err)
	}
	gated := func(deploy string) bool {
		for _, tr := range transitions {
			if stringInSlice(deploy, tr.BeforeServices()) {
				return true
			}
		}
		return false
	}
	for _, deploy := range []string{"foghorn", "chandler"} {
		if !gated(deploy) {
			t.Errorf("%s must be gated by a required transition (storage-descriptor-adoption) for v0.2.97; a direct upgrade must refuse it", deploy)
		}
	}
	if gated("purser") {
		t.Error("purser is downstream of no required transition and must NOT be refused by a direct upgrade")
	}
}

// TestPreflightReleaseTransitionBlockers pins that a direct `cluster upgrade --all` is refused BEFORE any deployment
// when its service list includes a transition-gated service — the zero-mutation guarantee, since this preflight runs
// before the deploy loop. For v0.2.97 storage-descriptor-adoption gates foghorn/chandler.
func TestPreflightReleaseTransitionBlockers(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true},
			"purser":  {Enabled: true},
		},
	}
	// A list that includes the gated service (foghorn) must be refused up front.
	if err := preflightReleaseTransitionBlockers(manifest, "v0.2.97", []string{"purser", "foghorn"}); err == nil {
		t.Fatal("upgrade --all including foghorn must be refused before any deploy (storage-descriptor-adoption gates it)")
	}
	// A list with no gated service proceeds (no refusal).
	if err := preflightReleaseTransitionBlockers(manifest, "v0.2.97", []string{"purser"}); err != nil {
		t.Fatalf("a service list with no transition-gated service must not be refused: %v", err)
	}
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
	if len(releaseTransitionRegistry) == 0 {
		t.Fatal("no compiled transitions registered; the check would be vacuous")
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

// TestAssertProvisionSatisfiesTransitions pins that a clean provision refuses a required transition it cannot satisfy
// by install (one that must be EXECUTED), routing it to release apply, while the real v0.2.97 transitions (established
// by Quartermaster bootstrap) pass.
func TestAssertProvisionSatisfiesTransitions(t *testing.T) {
	// A full-provision planned set: the storage transition's gated services (Foghorn) and its establishing service
	// (Quartermaster) are both scheduled, so the transition is applicable AND its bootstrap provider is present.
	planned := map[string]bool{"quartermaster": true, "foghorn": true}
	real, err := selectReleaseTransitions("v0.2.97")
	if err != nil {
		t.Fatalf("selectReleaseTransitions: %v", err)
	}
	if err := assertProvisionSatisfiesTransitions("v0.2.97", planned, real); err != nil {
		t.Fatalf("v0.2.97 transitions are established by bootstrap and must pass provision: %v", err)
	}
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
