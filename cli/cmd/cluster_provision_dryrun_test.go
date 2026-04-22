package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/artifacts"
)

func TestSummarizeDryRunDiff_emptyIsNoAnnotation(t *testing.T) {
	t.Parallel()
	got := summarizeDryRunDiff(artifacts.ConfigDiff{})
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestSummarizeDryRunDiff_allMatchReportsNoop(t *testing.T) {
	t.Parallel()
	diff := artifacts.ConfigDiff{
		Entries: []artifacts.ConfigDiffEntry{
			{Status: artifacts.StatusMatch},
			{Status: artifacts.StatusMatch},
		},
	}
	got := summarizeDryRunDiff(diff)
	if !strings.Contains(got, "no-op") {
		t.Errorf("want 'no-op', got %q", got)
	}
}

func TestSummarizeDryRunDiff_fileDifferReported(t *testing.T) {
	t.Parallel()
	diff := artifacts.ConfigDiff{
		Entries: []artifacts.ConfigDiffEntry{
			{Status: artifacts.StatusDiffer, Kind: artifacts.KindFileHash},
			{Status: artifacts.StatusDiffer, Kind: artifacts.KindFileHash},
		},
	}
	got := summarizeDryRunDiff(diff)
	if !strings.Contains(got, "would change") || !strings.Contains(got, "2 file(s) differ") {
		t.Errorf("want 'would change: 2 file(s) differ', got %q", got)
	}
}

func TestSummarizeDryRunDiff_envDifferReported(t *testing.T) {
	t.Parallel()
	diff := artifacts.ConfigDiff{
		Entries: []artifacts.ConfigDiffEntry{
			{Status: artifacts.StatusDiffer, Kind: artifacts.KindEnv, Env: &artifacts.EnvDiff{Added: []string{"K"}}},
		},
	}
	got := summarizeDryRunDiff(diff)
	if !strings.Contains(got, "1 env diff") {
		t.Errorf("want '1 env diff', got %q", got)
	}
}

func TestSummarizeDryRunDiff_mixedStatuses(t *testing.T) {
	t.Parallel()
	diff := artifacts.ConfigDiff{
		Entries: []artifacts.ConfigDiffEntry{
			{Status: artifacts.StatusMatch},
			{Status: artifacts.StatusDiffer, Kind: artifacts.KindEnv, Env: &artifacts.EnvDiff{Added: []string{"K"}}},
			{Status: artifacts.StatusMissingOnHost},
			{Status: artifacts.StatusProbeError},
		},
	}
	got := summarizeDryRunDiff(diff)
	for _, want := range []string{"1 env diff", "1 missing", "1 probe err"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q: %s", want, got)
		}
	}
}
