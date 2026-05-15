package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTagWellFormed(t *testing.T) {
	cases := []struct {
		in         string
		major      int
		minor      int
		patch      int
		rc         int
		isRC       bool
		wellFormed bool
	}{
		{"v0.2.39", 0, 2, 39, 0, false, true},
		{"v1.0.0", 1, 0, 0, 0, false, true},
		{"v0.2.40-rc1", 0, 2, 40, 1, true, true},
		{"v0.2.40-rc12", 0, 2, 40, 12, true, true},
		{"v1.2.3-rc0", 1, 2, 3, 0, true, true},
		{"v0.2", 0, 0, 0, 0, false, false},
		{"v0.2.3-alpha", 0, 0, 0, 0, false, false},
		{"v0.2.3-rc", 0, 0, 0, 0, false, false},
		{"random", 0, 0, 0, 0, false, false},
	}
	for _, tc := range cases {
		got := parseTag(tc.in)
		if got.wellFormed != tc.wellFormed {
			t.Errorf("%s: wellFormed = %v, want %v", tc.in, got.wellFormed, tc.wellFormed)
			continue
		}
		if !tc.wellFormed {
			continue
		}
		if got.major != tc.major || got.minor != tc.minor || got.patch != tc.patch || got.rc != tc.rc || got.isRC != tc.isRC {
			t.Errorf("%s: parsed %+v, want major=%d minor=%d patch=%d rc=%d isRC=%v",
				tc.in, got, tc.major, tc.minor, tc.patch, tc.rc, tc.isRC)
		}
	}
}

func TestParsedTagLessOrderingSemverPreReleases(t *testing.T) {
	// rc precedes stable of the same MMR; rcN < rcN+1; lower MMR < higher MMR.
	cases := []struct {
		a, b string
		less bool
	}{
		{"v0.2.39", "v0.2.40", true},
		{"v0.2.40-rc1", "v0.2.40-rc2", true},
		{"v0.2.40-rc1", "v0.2.40", true},
		{"v0.2.40", "v0.2.40-rc1", false},
		{"v0.2.40", "v0.2.40", false},
		{"v0.2.40-rc1", "v0.2.40-rc1", false},
		{"v0.2.40-rc2", "v0.2.40-rc1", false},
		{"v1.0.0-rc1", "v0.9.99", false},
	}
	for _, tc := range cases {
		a, b := parseTag(tc.a), parseTag(tc.b)
		got := a.less(b)
		if got != tc.less {
			t.Errorf("less(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.less)
		}
	}
}

func TestResolveBaselineStableToStable(t *testing.T) {
	releases := parseTagSlice("v0.2.37", "v0.2.38", "v0.2.39", "v0.2.39-rc1", "v0.2.39-rc2")
	baseline, lineage := resolveBaseline(parseTag("v0.2.40"), releases)
	if !baseline.wellFormed || baseline.raw != "v0.2.39" {
		t.Fatalf("baseline = %+v, want v0.2.39", baseline)
	}
	if len(lineage) != 1 || lineage[0].Track != "stable" {
		t.Fatalf("lineage = %+v, want one stable→stable step", lineage)
	}
}

func TestResolveBaselineRCToRC(t *testing.T) {
	releases := parseTagSlice("v0.2.39", "v0.2.40-rc1", "v0.2.40-rc2")
	baseline, lineage := resolveBaseline(parseTag("v0.2.40-rc3"), releases)
	if !baseline.wellFormed || baseline.raw != "v0.2.40-rc2" {
		t.Fatalf("baseline = %+v, want v0.2.40-rc2", baseline)
	}
	if len(lineage) != 1 || lineage[0].Track != "rc" {
		t.Fatalf("lineage = %+v, want one rc→rc step", lineage)
	}
}

func TestResolveBaselineRCToStablePromotion(t *testing.T) {
	releases := parseTagSlice("v0.2.39", "v0.2.40-rc1", "v0.2.40-rc2", "v0.2.40-rc3")
	baseline, lineage := resolveBaseline(parseTag("v0.2.40"), releases)
	if !baseline.wellFormed || baseline.raw != "v0.2.40-rc3" {
		t.Fatalf("baseline = %+v, want v0.2.40-rc3 (promotion)", baseline)
	}
	if len(lineage) != 1 || lineage[0].Track != "rc" {
		t.Fatalf("lineage = %+v, want rc→stable promotion step", lineage)
	}
}

func TestResolveBaselineStableToRCFallback(t *testing.T) {
	releases := parseTagSlice("v0.2.38", "v0.2.39") // no rc yet on 0.2.40
	baseline, lineage := resolveBaseline(parseTag("v0.2.40-rc1"), releases)
	if !baseline.wellFormed || baseline.raw != "v0.2.39" {
		t.Fatalf("baseline = %+v, want v0.2.39 (fallback)", baseline)
	}
	if len(lineage) != 1 || lineage[0].Track != "stable" {
		t.Fatalf("lineage = %+v, want stable→rc fallback step", lineage)
	}
}

func TestResolveBaselineFirstReleaseEver(t *testing.T) {
	baseline, lineage := resolveBaseline(parseTag("v0.1.0"), nil)
	if baseline.wellFormed {
		t.Fatalf("baseline = %+v, want zero-value", baseline)
	}
	if len(lineage) != 1 {
		t.Fatalf("lineage = %+v, want one explanatory step", lineage)
	}
}

func TestListReleasesIgnoresMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	releasesDir := filepath.Join(dir, "releases")
	if err := os.MkdirAll(releasesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"v0.2.39.yaml",
		"v0.2.40-rc1.yaml",
		"channel-stable.yaml", // not a release manifest
		"README.md",
		"backup.yaml.bak",
	} {
		if err := os.WriteFile(filepath.Join(releasesDir, name), []byte("platform_version: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := listReleases(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d well-formed releases, want 2: %+v", len(got), got)
	}
}

// parseTagSlice is a tiny test-only helper for building release lists.
func parseTagSlice(tags ...string) []parsedTag {
	out := make([]parsedTag, 0, len(tags))
	for _, t := range tags {
		out = append(out, parseTag(t))
	}
	return out
}
