package datamigrate

import (
	"context"
	"errors"
	"fmt"
)

// Requirement is the cluster-side view of one catalog-declared data
// migration. Mirrors releases.DataMigrationRequirement so this package
// stays free of cli/internal imports — callers translate.
type Requirement struct {
	ID                    string
	Service               string
	IntroducedIn          string
	RequiredBeforePhase   string
	RequiredBeforeVersion string
}

// LiveStatus is what the cluster CLI receives when querying a service for
// one migration's state. Service is set so callers can format messages even
// when the service did not respond.
type LiveStatus struct {
	ID      string
	Service string

	// Status is the resolved JobState.Status, or "" when state could not be
	// retrieved (Reason is set in that case).
	Status Status

	// NotRegistered means the service binary responded but reported the id
	// is not in its compiled-in registry.
	NotRegistered bool

	// NotAdopted means the service binary does not support `data-migrations`
	// at all (no adoption of pkg/datamigrate).
	NotAdopted bool

	// FetchError is any non-recoverable transport/exec error.
	FetchError error
}

// Reportable returns true when Status reflects retrieved state. False means
// the migration is unreportable (NotRegistered, NotAdopted, or FetchError) —
// every gate treats that as a blocker.
func (s LiveStatus) Reportable() bool {
	return s.FetchError == nil && !s.NotRegistered && !s.NotAdopted
}

// StateSource returns the live status of one (service, id) pair. The cluster
// CLI provides this; tests pass an in-memory fake. A nil return value with a
// nil error is invalid — callers should always populate Status, NotRegistered,
// NotAdopted, or FetchError.
type StateSource func(ctx context.Context, service, id string) LiveStatus

// Blocker is one entry in a refusal — a required migration that is not
// completed for whatever reason (pending, failed, unreportable).
type Blocker struct {
	Requirement Requirement
	Live        LiveStatus
	// Reason is a short human-readable explanation. Examples:
	// "pending", "failed: <msg>", "service binary has not adopted data-migrations".
	Reason string
}

// PreDeployBlockers returns every required PRIOR-version data migration
// across (currentVersion, targetVersion] that is not completed. "Prior"
// means RequiredBeforeVersion <= targetVersion AND IntroducedIn != targetVersion.
//
// The pre-deploy gate cannot require a target-version data migration to be
// completed because completing it requires the target's code. Target data
// migrations are gated by PrePostdeployBlockers.
//
// An empty result with N declared requirements is honest: "N required
// migrations completed". An empty result with 0 declared requirements is
// also honest: "no required prior data migrations declared in this window."
// Callers must surface these distinctly.
func PreDeployBlockers(ctx context.Context, src StateSource, reqs []Requirement, currentVersion, targetVersion string, semverCompare func(a, b string) int) ([]Blocker, error) {
	if src == nil {
		return nil, errors.New("PreDeployBlockers: nil StateSource")
	}
	if semverCompare == nil {
		return nil, errors.New("PreDeployBlockers: nil semverCompare")
	}
	var out []Blocker
	for _, r := range reqs {
		if r.IntroducedIn == "" || r.IntroducedIn == targetVersion {
			continue
		}
		// IntroducedIn relative to currentVersion is informational only:
		// even migrations introduced before currentVersion can still block
		// when their RequiredBeforeVersion lies inside our (current, target]
		// window. The RequiredBeforeVersion gate below is what decides.
		_ = currentVersion

		// RequiredBeforeVersion gate: if set, only block when target is at or
		// past that version. If unset, treat as "blocks anything past
		// IntroducedIn", which means pre-deploy of any later release.
		rbv := r.RequiredBeforeVersion
		if rbv == "" {
			// Default: required before the next release after IntroducedIn,
			// which we approximate as "any target > IntroducedIn".
			if semverCompare(targetVersion, r.IntroducedIn) <= 0 {
				continue
			}
		} else if semverCompare(targetVersion, rbv) < 0 {
			continue
		}

		live := src(ctx, r.Service, r.ID)
		if blocker, blocked := classify(r, live); blocked {
			out = append(out, blocker)
		}
	}
	return out, nil
}

// PrePostdeployBlockers returns every required TARGET-version data migration
// (RequiredBeforePhase = "postdeploy") that is not completed. Used to gate
// `cluster migrate --phase postdeploy --to-version vT`. Same fail-closed
// semantics as PreDeployBlockers.
func PrePostdeployBlockers(ctx context.Context, src StateSource, reqs []Requirement, targetVersion string, semverCompare func(a, b string) int) ([]Blocker, error) {
	return PrePhaseBlockers(ctx, src, reqs, "postdeploy", targetVersion, semverCompare)
}

// PrePhaseBlockers returns every required data migration for phase whose
// introduced version is at or before targetVersion and whose live state is not
// completed. Used before postdeploy and contract SQL phases.
func PrePhaseBlockers(ctx context.Context, src StateSource, reqs []Requirement, phase, targetVersion string, semverCompare func(a, b string) int) ([]Blocker, error) {
	if src == nil {
		return nil, errors.New("PrePhaseBlockers: nil StateSource")
	}
	if semverCompare == nil {
		return nil, errors.New("PrePhaseBlockers: nil semverCompare")
	}
	if phase == "" {
		return nil, errors.New("PrePhaseBlockers: empty phase")
	}
	var out []Blocker
	for _, r := range reqs {
		if r.RequiredBeforePhase != phase {
			continue
		}
		if semverCompare(r.IntroducedIn, targetVersion) > 0 {
			continue
		}
		live := src(ctx, r.Service, r.ID)
		if blocker, blocked := classify(r, live); blocked {
			out = append(out, blocker)
		}
	}
	return out, nil
}

func classify(r Requirement, live LiveStatus) (Blocker, bool) {
	switch {
	case live.FetchError != nil:
		return Blocker{Requirement: r, Live: live, Reason: fmt.Sprintf("fetch failed: %v", live.FetchError)}, true
	case live.NotAdopted:
		return Blocker{Requirement: r, Live: live, Reason: "service binary has not adopted data-migrations"}, true
	case live.NotRegistered:
		return Blocker{Requirement: r, Live: live, Reason: "declared in catalog but not registered in service binary"}, true
	case live.Status == StatusCompleted:
		return Blocker{}, false
	case live.Status == "":
		return Blocker{Requirement: r, Live: live, Reason: "no live state reported"}, true
	default:
		return Blocker{Requirement: r, Live: live, Reason: string(live.Status)}, true
	}
}
