package cmd

import (
	"errors"
	"slices"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/provisioner"
)

// testDiscard is an io.Writer sink for cobra command output in gate/classification tests.
type testDiscard struct{}

func (testDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestFirstIncompletePriorRelease(t *testing.T) {
	catalog := []releases.Release{{Version: "v0.1.0"}, {Version: "v0.2.0"}, {Version: "v0.3.0"}, {Version: "v0.4.0"}}
	below := func(target string) []releases.Release {
		var out []releases.Release
		for _, rel := range catalog {
			if releases.CompareSemver(releases.BaseVersion(rel.Version), releases.BaseVersion(target)) < 0 {
				out = append(out, rel)
			}
		}
		return out
	}
	// ledgerThrough models a cumulative ledger that has every postdeploy migration up to and including `applied`.
	ledgerThrough := func(applied string) func(string) ([]provisioner.MigrationKey, error) {
		return func(version string) ([]provisioner.MigrationKey, error) {
			var missing []provisioner.MigrationKey
			for _, rel := range catalog {
				if releases.CompareSemver(rel.Version, applied) > 0 && releases.CompareSemver(rel.Version, version) <= 0 {
					missing = append(missing, provisioner.MigrationKey{Database: "foghorn", Version: rel.Version, Phase: "postdeploy", Filename: "001.sql"})
				}
			}
			return missing, nil
		}
	}

	tests := []struct {
		name        string
		target      string
		applied     string
		wantRelease string
		wantMissing []string
	}{
		{name: "complete through an earlier release names the next one", target: "v0.4.0", applied: "v0.1.0", wantRelease: "v0.2.0", wantMissing: []string{"v0.2.0"}},
		{name: "only the highest prior release incomplete", target: "v0.4.0", applied: "v0.2.0", wantRelease: "v0.3.0", wantMissing: []string{"v0.3.0"}},
		{name: "nothing applied names the lowest release", target: "v0.4.0", applied: "v0.0.0", wantRelease: "v0.1.0", wantMissing: []string{"v0.1.0"}},
		{name: "all prior releases complete", target: "v0.4.0", applied: "v0.3.0"},
		{name: "target's own release is excluded", target: "v0.4.0-rc1", applied: "v0.3.0"},
		{name: "no prior release", target: "v0.1.0", applied: "v0.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var checked []string
			ledger := ledgerThrough(tt.applied)
			release, missing, err := firstIncompletePriorRelease(tt.target, below, func(v string) ([]provisioner.MigrationKey, error) {
				checked = append(checked, v)
				return ledger(v)
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if release != tt.wantRelease {
				t.Fatalf("release = %q, want %q", release, tt.wantRelease)
			}
			var gotMissing []string
			for _, m := range missing {
				gotMissing = append(gotMissing, m.Version)
			}
			if !slices.Equal(gotMissing, tt.wantMissing) {
				t.Fatalf("missing = %v, want %v", gotMissing, tt.wantMissing)
			}
			for _, v := range checked {
				if releases.BaseVersion(v) == releases.BaseVersion(tt.target) {
					t.Fatalf("checked the target's own release %s; its postdeploy runs after deploy", v)
				}
			}
		})
	}

	t.Run("check error propagates", func(t *testing.T) {
		boom := errors.New("ledger unreachable")
		release, missing, err := firstIncompletePriorRelease("v0.4.0", below, func(string) ([]provisioner.MigrationKey, error) {
			return nil, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped %v", err, boom)
		}
		if release != "" || missing != nil {
			t.Fatalf("an error must not report a release: release=%q missing=%v", release, missing)
		}
	})

	t.Run("check error on an earlier release propagates", func(t *testing.T) {
		boom := errors.New("ledger unreachable")
		_, _, err := firstIncompletePriorRelease("v0.4.0", below, func(v string) ([]provisioner.MigrationKey, error) {
			if v == "v0.2.0" {
				return nil, boom
			}
			return ledgerThrough("v0.1.0")(v)
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped %v", err, boom)
		}
	})

	t.Run("embedded catalog excludes the target's own release", func(t *testing.T) {
		all := releases.ReleasesBelow("v999.0.0")
		if len(all) < 2 {
			t.Skip("embedded catalog has fewer than two releases")
		}
		target := all[len(all)-1].Version
		var checked []string
		_, _, err := firstIncompletePriorRelease(target, releases.ReleasesBelow, func(v string) ([]provisioner.MigrationKey, error) {
			checked = append(checked, v)
			return nil, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if slices.Contains(checked, target) {
			t.Fatalf("checked the target %s itself: %v", target, checked)
		}
		if len(checked) != 1 || checked[0] != all[len(all)-2].Version {
			t.Fatalf("a complete ledger must be proven by the highest prior release only, checked %v", checked)
		}
	})
}
