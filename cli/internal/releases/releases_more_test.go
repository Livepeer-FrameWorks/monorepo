package releases

import (
	"testing"
)

func TestParseSemver_Components(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in            string
		maj, min, pat int
		pre           string
	}{
		{"v1.2.3", 1, 2, 3, ""},
		{"1.2.3", 1, 2, 3, ""},
		{"v0.10.7", 0, 10, 7, ""},
		{"v1.2.3-rc1", 1, 2, 3, "rc1"}, // '-' separator, full tag preserved
		{"v1.2.3-alpha.2", 1, 2, 3, "alpha.2"},
		{"v1.2.3+build9", 1, 2, 3, "build9"}, // '+' build metadata uses same split
		{"v2", 2, 0, 0, ""},                  // missing minor/patch default 0
		{"v2.5", 2, 5, 0, ""},
		{"v-rc1", 0, 0, 0, "rc1"},   // separator at index 0 (i>=0 boundary)
		{"v+meta", 0, 0, 0, "meta"}, // '+' separator at index 0
	}
	for _, c := range cases {
		maj, min, pat, pre := parseSemver(c.in)
		if maj != c.maj || min != c.min || pat != c.pat || pre != c.pre {
			t.Errorf("parseSemver(%q) = (%d,%d,%d,%q), want (%d,%d,%d,%q)",
				c.in, maj, min, pat, pre, c.maj, c.min, c.pat, c.pre)
		}
	}
}

func TestParseSemver_PrereleaseExactTag(t *testing.T) {
	t.Parallel()
	// The whole substring after the separator must be the tag (catches an
	// off-by-one in the v[i+1:] slice).
	_, _, _, pre := parseSemver("v1.0.0-rc1")
	if pre != "rc1" {
		t.Fatalf("prerelease tag must be exactly %q; got %q", "rc1", pre)
	}
}

func TestCompareSemver_BuildMetadataAndPrereleaseTagLength(t *testing.T) {
	t.Parallel()
	// rc1 vs rc10 differ only in the trailing chars of the full tag; an
	// off-by-one slice would compare truncated tags and get the wrong sign.
	if got := CompareSemver("v1.0.0-rc1", "v1.0.0-rc10"); got != -1 {
		t.Fatalf("rc1 < rc10 expected -1; got %d", got)
	}
}

func TestCatalog_ReturnsNonNilSlice(t *testing.T) {
	// Catalog must return an initialized (non-nil) slice on a clean load, so
	// callers can range/append without a nil guard.
	if got := Catalog(); got == nil {
		t.Fatal("Catalog() must return a non-nil slice when the catalog loads cleanly")
	}
}
