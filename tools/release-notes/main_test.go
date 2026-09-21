package main

import (
	"strings"
	"testing"
)

func TestRenderGroupsCommitsBySection(t *testing.T) {
	commits := []commit{
		{Hash: "a1", Subject: "feat(player): add low-latency mode"},
		{Hash: "b2", Subject: "fix(media): separate buffer playability from health"},
		{Hash: "c3", Subject: "chore(release): prepare v0.3.5"},
		{Hash: "d4", Subject: "refactor!: replace env helpers with typed config"},
		{Hash: "e5", Subject: "docs: add release lifecycle page"},
		{Hash: "f6", Subject: "ci(release): classify tags through pkg/version"},
		{Hash: "g7", Subject: "Merge the thing"},
	}
	out := render("v0.3.5", "v0.3.4", "HEAD", commits)

	for _, want := range []string{
		"# Release v0.3.5",
		"## Upgrade",
		"## New\n\n- add low-latency mode (player, a1)",
		"## Hardened\n\n- **Breaking:** replace env helpers with typed config (d4)",
		"## Fixes\n\n- separate buffer playability from health (media, b2)",
		"## Build / infra\n\n- classify tags through pkg/version (release, f6)",
		"## Docs\n\n- add release lifecycle page (e5)",
		"## Unclassified\n\n- Merge the thing (g7)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("draft missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "prepare v0.3.5") {
		t.Error("release preparation commits must be skipped")
	}
	order := []string{"## Upgrade", "## New", "## Hardened", "## Fixes", "## Build / infra", "## Docs", "## Unclassified"}
	last := -1
	for _, heading := range order {
		idx := strings.Index(out, heading)
		if idx < last {
			t.Fatalf("section %q is out of order", heading)
		}
		last = idx
	}
}

func TestRenderDropsEmptySections(t *testing.T) {
	out := render("", "v0.3.4", "HEAD", []commit{{Hash: "a1", Subject: "fix: one fix"}})
	if strings.Contains(out, "## New") || strings.Contains(out, "## Docs") {
		t.Fatalf("empty sections rendered:\n%s", out)
	}
	if !strings.Contains(out, "# Release vX.Y.Z") {
		t.Fatalf("missing placeholder title:\n%s", out)
	}
}
