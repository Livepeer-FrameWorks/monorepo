package cmd

import (
	"strings"
	"testing"
)

func TestSummarizeYugabyteLayoutWarnsOnDrift(t *testing.T) {
	matching := yugabyteLayoutReport{Database: "quartermaster", Declared: "colocated", Observed: "colocated", Tablets: 7, Peers: 21}
	drifted := yugabyteLayoutReport{
		Database: "foghorn_eu", Declared: "colocated", Observed: "distributed", Tablets: 120, Peers: 340,
		Drift:            []string{"database is distributed, layout declares colocated"},
		UnreachableNodes: []string{"yuga-eu-3"},
	}

	result := summarizeYugabyteLayout([]yugabyteLayoutReport{drifted, matching})
	if result.OK || result.Status != "degraded" {
		t.Fatalf("drift with incomplete tablet evidence = ok %v status %q, want degraded", result.OK, result.Status)
	}
	if !strings.Contains(result.Error, "foghorn_eu: database is distributed, layout declares colocated") {
		t.Fatalf("drift error = %q", result.Error)
	}
	onlyDrift := drifted
	onlyDrift.UnreachableNodes = nil
	warning := summarizeYugabyteLayout([]yugabyteLayoutReport{onlyDrift, matching})
	if warning.OK || warning.Status != yugabyteLayoutWarning || !strings.Contains(warning.Error, "until they are relaid out") {
		t.Fatalf("drift alone = %+v, want a warning, not a failure", warning)
	}
	if step := doctorServiceRemediation("Yugabyte layout"); !strings.Contains(step.Cmd, "relayout plan") {
		t.Fatalf("layout remediation = %+v, want relayout plan", step)
	}
	if got := result.Metadata["quartermaster"]; got != "layout=colocated observed=colocated tablets=7 peers=21 drift=0" {
		t.Fatalf("quartermaster metadata = %q", got)
	}
	if got := result.Metadata["foghorn_eu"]; !strings.HasSuffix(got, "tablet_counts_exclude=yuga-eu-3") {
		t.Fatalf("foghorn_eu metadata = %q, want unreachable nodes noted", got)
	}

	clean := summarizeYugabyteLayout([]yugabyteLayoutReport{matching})
	if !clean.OK || clean.Status != "healthy" || clean.Message != "1 database(s) match their declared layout" {
		t.Fatalf("matching layout result = %+v", clean)
	}

	partial := matching
	partial.UnreachableNodes = []string{"yuga-eu-2"}
	incomplete := summarizeYugabyteLayout([]yugabyteLayoutReport{partial})
	if incomplete.OK || incomplete.Status != "degraded" || !strings.Contains(incomplete.Error, "tablet evidence is incomplete (quartermaster: tablets not read from yuga-eu-2)") {
		t.Fatalf("matching layout with an unreachable tserver = %+v, want degraded incomplete evidence", incomplete)
	}
}

func TestSummarizeYugabyteLayoutTruncatesLongDrift(t *testing.T) {
	report := yugabyteLayoutReport{Database: "purser", Declared: "colocated", Observed: "colocated", Drift: []string{"a", "b", "c", "d", "e"}}
	result := summarizeYugabyteLayout([]yugabyteLayoutReport{report})
	if !strings.Contains(result.Error, "purser: a; b; c; 2 more") {
		t.Fatalf("drift error = %q, want the first three entries and a remainder count", result.Error)
	}
}

func TestSummarizeYugabyteLayoutKeepsOtherDatabasesWhenOneIsMissingOrUnreadable(t *testing.T) {
	matching := yugabyteLayoutReport{Database: "quartermaster", Declared: "colocated", Observed: "colocated", Tablets: 7, Peers: 21}
	missing := yugabyteLayoutReport{Database: "lookout", Missing: true}
	result := summarizeYugabyteLayout([]yugabyteLayoutReport{matching, missing})
	if !result.OK || result.Metadata["lookout"] != "not created yet" || !strings.Contains(result.Message, "not created yet: lookout") {
		t.Fatalf("missing database result = %+v, want a healthy check that names it", result)
	}
	failed := yugabyteLayoutReport{Database: "purser", Err: "read relation placement: timeout"}
	result = summarizeYugabyteLayout([]yugabyteLayoutReport{matching, failed})
	if result.OK || result.Metadata["quartermaster"] == "" || !strings.Contains(result.Error, "could not inspect purser") {
		t.Fatalf("unreadable database result = %+v, want the others reported and the failure named", result)
	}
}
