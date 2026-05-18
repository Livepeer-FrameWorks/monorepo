package control

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoClipPlusNamespace enforces invariant #7: clips ride the same
// vod+<internal_name> Mist namespace as VOD. Any "clip+" literal in the
// codebase is the wrong prefix sneaking back in and must fail the build.
//
// Walks the monorepo from this package upward. Excludes vendor and the
// monorepo-foreign trees that shouldn't be scanned (the MistServer and
// gitops checkouts that live alongside the monorepo). Skips this file
// itself so the literal in the assertion below doesn't self-trigger.
func TestNoClipPlusNamespace(t *testing.T) {
	root := monorepoRootFromHere(t)
	skipDirs := map[string]struct{}{
		"node_modules": {},
		"vendor":       {},
		".git":         {},
		"dist":         {},
		"bin":          {},
		"build":        {},
	}
	skipFiles := map[string]struct{}{
		filepath.Base(thisFile()): {},
	}
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if _, skip := skipFiles[d.Name()]; skip {
			return nil
		}
		ext := filepath.Ext(d.Name())
		switch ext {
		case ".go", ".proto", ".graphql", ".sql", ".ts", ".tsx", ".js", ".svelte":
		default:
			return nil
		}
		// Generated proto/graphql code may legitimately serialize unrelated
		// strings that contain "clip+" as a substring of larger tokens; we
		// only care about the word as a Mist stream-name prefix, which is
		// always immediately followed by an identifier character or a
		// quote-closing context. Be conservative: match `"clip+`.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr // unreadable files don't bear evidence; keep walking
		}
		if strings.Contains(string(data), `"clip+`) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(hits) > 0 {
		t.Fatalf("clip+ namespace is banned (clips use vod+<internal_name>); found in: %s", strings.Join(hits, ", "))
	}
}

// TestResolveStreamDVRPrefix_NilCommodore pins the new dvr+ branch
// shape: the only legal dvr+ token is dvr+<dvr_internal_name>, and
// without a Commodore client wired (test default) the resolver cannot
// confirm that — so every dvr+ input fails closed with an error and
// never silently rewrites to vod+. The legacy dvr+<chapter_id> shape
// is intentionally rejected: chapters address themselves through the
// chapter artifact's public playback_id, not through dvr+.
func TestResolveStreamDVRPrefix_NilCommodore(t *testing.T) {
	prev := CommodoreClient
	CommodoreClient = nil
	defer func() { CommodoreClient = prev }()

	cases := []struct {
		name  string
		input string
	}{
		{name: "dvr+<internal_name> rejected without Commodore", input: "dvr+dvr_int_001"},
		{name: "dvr+<chapter_id> rejected", input: "dvr+chapter-abc"},
		{name: "empty dvr+ rejected", input: "dvr+"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt, err := ResolveStream(context.Background(), tc.input)
			if err == nil {
				t.Fatalf("expected error for %q, got target: %+v", tc.input, tgt)
			}
			if tgt == nil {
				t.Fatalf("expected sentinel empty target alongside error, got nil")
			}
			if tgt.InternalName != "" {
				t.Fatalf("expected empty InternalName on rejection, got %q", tgt.InternalName)
			}
		})
	}
	_ = strings.HasPrefix // keep import in case of future assertions
}

// monorepoRootFromHere returns the absolute path of the FrameWorks
// monorepo root by walking upward from this test file until a
// go.work or root-marker file is found.
func monorepoRootFromHere(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for i := 0; i < 12; i++ {
		for _, marker := range []string{"go.work", "Makefile"} {
			if _, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil {
				// Both Makefile and go.work appear in subdirectories too;
				// prefer the highest-level go.work, fall back to Makefile +
				// the presence of the api_balancing and pkg subdirs.
				if _, a := os.Stat(filepath.Join(dir, "api_balancing")); a == nil {
					if _, p := os.Stat(filepath.Join(dir, "pkg")); p == nil {
						return dir
					}
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate monorepo root from %s", cwd)
	return ""
}

// thisFile returns the source filename of this test (for self-exclusion
// in the grep walk).
func thisFile() string {
	return "dvr_routing_invariants_test.go"
}
