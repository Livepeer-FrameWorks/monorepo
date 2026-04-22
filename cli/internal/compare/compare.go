package compare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"frameworks/cli/pkg/artifacts"
)

// CompareTarget runs every DesiredArtifact through the Runner. A probe
// failure on one path does not short-circuit the rest.
func CompareTarget(ctx context.Context, target artifacts.TargetID, desired []artifacts.DesiredArtifact, runner Runner) artifacts.ConfigDiff {
	diff := artifacts.ConfigDiff{Target: target}
	for _, a := range desired {
		diff.Entries = append(diff.Entries, compareOne(ctx, a, runner))
	}
	return diff
}

func compareOne(ctx context.Context, a artifacts.DesiredArtifact, runner Runner) artifacts.ConfigDiffEntry {
	entry := artifacts.ConfigDiffEntry{Path: a.Path, Kind: a.Kind}

	observed, missing, err := runner.Fetch(ctx, a.Path)
	if err != nil {
		entry.Status = artifacts.StatusProbeError
		entry.Detail = err.Error()
		return entry
	}
	if missing {
		entry.Status = artifacts.StatusMissingOnHost
		return entry
	}

	switch a.Kind {
	case artifacts.KindFileHash:
		if sha256.Sum256(observed) == sha256.Sum256(a.Content) {
			entry.Status = artifacts.StatusMatch
		} else {
			entry.Status = artifacts.StatusDiffer
		}

	case artifacts.KindEnv:
		desiredMap := artifacts.ParseEnvBytes(a.Content)
		observedMap := artifacts.ParseEnvBytes(observed)
		for _, k := range a.IgnoreKeys {
			delete(desiredMap, k)
			delete(observedMap, k)
		}
		envDiff := diffEnvMaps(desiredMap, observedMap)
		entry.Env = &envDiff
		if envDiff.HasDifferences() {
			entry.Status = artifacts.StatusDiffer
		} else {
			entry.Status = artifacts.StatusMatch
		}

	case artifacts.KindManagedInvariant:
		missingSubs := checkInvariants(observed, a.Invariant)
		if len(missingSubs) == 0 {
			entry.Status = artifacts.StatusMatch
		} else {
			entry.Status = artifacts.StatusDiffer
			entry.Detail = "missing invariant(s): " + renderSubs(missingSubs)
		}
	}
	return entry
}

// diffEnvMaps computes key-level delta. Added = in desired, not observed;
// Removed = in observed, not desired; Changed = in both with different
// values. No values are copied into the EnvDiff.
func diffEnvMaps(desired, observed map[string]string) artifacts.EnvDiff {
	diff := artifacts.EnvDiff{}
	for k := range desired {
		if _, ok := observed[k]; !ok {
			diff.Added = append(diff.Added, k)
		}
	}
	for k := range observed {
		if _, ok := desired[k]; !ok {
			diff.Removed = append(diff.Removed, k)
		}
	}
	for k, dv := range desired {
		if ov, ok := observed[k]; ok && ov != dv {
			diff.Changed = append(diff.Changed, k)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Changed)
	return diff
}

func checkInvariants(content []byte, inv *artifacts.Invariant) [][]byte {
	if inv == nil {
		return nil
	}
	var missing [][]byte
	for _, sub := range inv.MustContain {
		if !bytes.Contains(content, sub) {
			missing = append(missing, sub)
		}
	}
	return missing
}

func renderSubs(subs [][]byte) string {
	parts := make([]string, len(subs))
	for i, s := range subs {
		parts[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(parts, ", ")
}
