package releases

import (
	"testing"
)

func TestSplitSemver_Components(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in                 string
		maj, min, pat, pre string
	}{
		{"v1.2.3", "1", "2", "3", ""},
		{"1.2.3", "1", "2", "3", ""},
		{"v0.10.7", "0", "10", "7", ""},
		{"v1.2.3-rc1", "1", "2", "3", "rc1"}, // '-' separator, full tag preserved
		{"v1.2.3-alpha.2", "1", "2", "3", "alpha.2"},
		{"v1.2.3+build9", "1", "2", "3", ""},        // '+' build metadata is stripped (no precedence effect, SemVer clause 10)
		{"v1.2.3-rc1+build9", "1", "2", "3", "rc1"}, // build metadata stripped, prerelease preserved
		{"v2", "2", "0", "0", ""},                   // missing minor/patch default "0"
		{"v2.5", "2", "5", "0", ""},
		{"v-rc1", "", "0", "0", "rc1"}, // separator at index 0: empty core component, prerelease preserved
		{"v+meta", "", "0", "0", ""},   // '+' build metadata stripped even at index 0
	}
	for _, c := range cases {
		maj, min, pat, pre := splitSemver(c.in)
		if maj != c.maj || min != c.min || pat != c.pat || pre != c.pre {
			t.Errorf("splitSemver(%q) = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
				c.in, maj, min, pat, pre, c.maj, c.min, c.pat, c.pre)
		}
	}
}

func TestSplitSemver_PrereleaseExactTag(t *testing.T) {
	t.Parallel()
	// The whole substring after the separator must be the tag (catches an
	// off-by-one in the v[i+1:] slice).
	_, _, _, pre := splitSemver("v1.0.0-rc1")
	if pre != "rc1" {
		t.Fatalf("prerelease tag must be exactly %q; got %q", "rc1", pre)
	}
}

// TestCompareSemver_NoIntegerOverflow pins the overflow-safety of the numeric comparison: a component far beyond int64
// range must still order by numeric magnitude, not truncate-to-zero and mis-order.
func TestCompareSemver_NoIntegerOverflow(t *testing.T) {
	t.Parallel()
	huge := "v99999999999999999999999999.0.0" // 26 digits, well past int64
	if got := CompareSemver(huge, "v2.0.0"); got != 1 {
		t.Fatalf("CompareSemver(%q, v2.0.0) = %d, want 1 (huge > small)", huge, got)
	}
	if got := CompareSemver("v1.0.0-rc99999999999999999999", "v1.0.0-rc2"); got != 1 {
		t.Fatalf("huge rc identifier must order above rc2, got %d", got)
	}
}

// TestValidateVersion_SharedValidator asserts the single exported validator every version-consuming gate routes
// through: it accepts canonical spellings and rejects the malformed values the previous permissive migration/release
// patterns let through (v1.0.0-, v1.0.0-., v1.2.3.4, leading zeros, and channel names / missing v).
func TestValidateVersion_SharedValidator(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"v1.0.0", "v0.2.97", "v0.2.97-rc1", "v1.0.0-rc.1", "v1.0.0+build.1", "v1.0.0-0"} {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("ValidateVersion(%q) should pass, got %v", v, err)
		}
	}
	for _, v := range []string{
		"1.0.0", "v1.0.0-", "v1.0.0-.", "v1.2.3.4", "v1.0.0-01", "v01.0.0", "stable", "v1.0", "",
		" v1.2.3", "v1.2.3 ", " v1.2.3 ", // surrounding whitespace: byte-exact rejection (callers reuse the raw value)
		"v1.0.0-rc01", // leading zero in the trailing rcN run (would otherwise compare equal to rc1)
	} {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("ValidateVersion(%q) should fail", v)
		}
	}
}

// TestParseCatalog_RejectsMalformedSemver pins the strict-validation completeness: leading-zero and empty-identifier
// spellings the permissive regex would accept are rejected before entering the catalog.
func TestParseCatalog_RejectsMalformedSemver(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"v1.0.0-01", "v1.0.0-.", "v1.0.0-rc.", "v01.0.0", "v1.00.0", "v1.0.0-a..b"} {
		if cs := parseCatalog([]byte("releases:\n  - version: " + v + "\n")); cs.err == nil {
			t.Errorf("malformed version %q must be rejected", v)
		}
	}
	for _, v := range []string{"v1.0.0", "v0.2.97", "v1.0.0-rc1", "v1.0.0-rc.1", "v1.0.0-0", "v1.0.0+build.1"} {
		if cs := parseCatalog([]byte("releases:\n  - version: " + v + "\n")); cs.err != nil {
			t.Errorf("valid version %q must pass: %v", v, cs.err)
		}
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

// TestCompareSemver_PrereleasePrecedence pins SemVer clause 11 precedence, especially the numeric ordering that keeps the
// min-CLI floor honest: a lexical comparator would put rc10 < rc2 and let an rc2 CLI through an rc10 floor.
func TestCompareSemver_PrereleasePrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0-rc2", "v1.0.0-rc10", -1},   // combined form: 2 < 10 numerically, NOT lexically
		{"v1.0.0-rc10", "v1.0.0-rc2", 1},    // symmetric
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1}, // dotted numeric identifier: 2 < 10
		{"v1.0.0-rc.10", "v1.0.0-rc.2", 1},
		{"v1.0.0-rc2", "v1.0.0-rc2", 0},
		{"v1.0.0-alpha", "v1.0.0-beta", -1},    // alphanumeric lexical
		{"v1.0.0-1", "v1.0.0-alpha", -1},       // numeric identifier < alphanumeric
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1}, // fewer identifiers < more
		{"v1.0.0-rc1", "v1.0.0", -1},           // any prerelease < no prerelease
		{"v1.0.0", "v1.0.0-rc1", 1},            // symmetric
		{"v1.0.0-rc2+build", "v1.0.0-rc2", 0},  // build metadata ignored
		{"v1.0.0+build", "v1.0.0", 0},          // build metadata ignored on the release
		{"v1.0.0-rc10+a", "v1.0.0-rc2+b", 1},   // build ignored, 10 > 2
	}
	for _, c := range cases {
		if got := CompareSemver(c.a, c.b); got != c.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestParseCatalog_RequiresCanonicalVPrefix asserts the catalog must use the canonical
// `v`-prefixed spelling, so `1.0.0` (no `v`) cannot be declared and evade the one-entry-per-release-line check.
func TestParseCatalog_RequiresCanonicalVPrefix(t *testing.T) {
	t.Parallel()
	if cs := parseCatalog([]byte("releases:\n  - version: 1.0.0\n")); cs.err == nil {
		t.Fatal("a non-canonical (no leading v) release version must be rejected")
	}
	// The spelling-collision the canonicalized BaseVersion + v-required validation prevent: even if only base
	// identity mattered, v1.0.0 and 1.0.0 must never both be accepted. (1.0.0 is already rejected above.)
	if cs := parseCatalog([]byte("releases:\n  - version: v1.0.0\n")); cs.err != nil {
		t.Fatalf("canonical v-prefixed version must pass: %v", cs.err)
	}
}

func TestCatalog_ReturnsNonNilSlice(t *testing.T) {
	// Catalog must return an initialized (non-nil) slice on a clean load, so
	// callers can range/append without a nil guard.
	if got := Catalog(); got == nil {
		t.Fatal("Catalog() must return a non-nil slice when the catalog loads cleanly")
	}
}

func TestBaseVersion(t *testing.T) {
	cases := map[string]string{
		"v0.2.97":       "v0.2.97",
		"v0.2.97-rc1":   "v0.2.97",
		"v0.2.97-rc.2":  "v0.2.97",
		"v0.2.97+build": "v0.2.97",
		// The leading `v` is CANONICALIZED: a no-`v` spelling normalizes to the same base identity as its v-prefixed
		// form, so the two cannot resolve differently or evade the one-entry-per-release-line check.
		"0.2.97-rc1": "v0.2.97",
		"0.2.97":     "v0.2.97",
	}
	for in, want := range cases {
		if got := BaseVersion(in); got != want {
			t.Fatalf("BaseVersion(%q)=%q want %q", in, got, want)
		}
	}
	// A canary sorts BEFORE its final, but BaseVersion normalizes both to equal.
	if CompareSemver(BaseVersion("v0.2.97-rc1"), "v0.2.97") != 0 {
		t.Fatalf("base of a canary must equal the final version")
	}
}
