package streamident

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in           string
		wantKind     Kind
		wantConcrete string
	}{
		{"live+abc123", KindSourceLive, "abc123"},
		{"pull+abc123", KindSourcePull, "abc123"},
		{"vod+artifactX", KindArtifactVOD, "artifactX"},
		{"dvr+dvrArtifactY", KindArtifactDVR, "dvrArtifactY"},
		{"processing+202605abc", KindArtifactProcessing, "202605abc"},

		// Bare names — parser cannot disambiguate.
		{"abc123", KindBare, "abc123"},
		{"frameworks-demo", KindBare, "frameworks-demo"},
		{"", KindBare, ""},

		// Edge: prefix-like but not exact (no `+`) is bare.
		{"liveabc", KindBare, "liveabc"},
		{"vodartifact", KindBare, "vodartifact"},

		// Edge: prefix with empty concrete (Mist would never emit this,
		// but we shouldn't panic). Classified by prefix; Concrete is "".
		{"live+", KindSourceLive, ""},
		{"vod+", KindArtifactVOD, ""},

		// Edge: prefix in the middle of the string is not a match —
		// CutPrefix only matches at the start.
		{"foo-live+bar", KindBare, "foo-live+bar"},

		// Edge: nested-looking prefixes parse only the first prefix.
		// Caller must not feed already-parsed names back in.
		{"live+vod+x", KindSourceLive, "vod+x"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := Parse(tc.in)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			if got.Concrete != tc.wantConcrete {
				t.Errorf("Concrete = %q, want %q", got.Concrete, tc.wantConcrete)
			}
			if got.Original != tc.in {
				t.Errorf("Original = %q, want %q", got.Original, tc.in)
			}
		})
	}
}

func TestKindHelpers(t *testing.T) {
	cases := []struct {
		k            Kind
		wantSource   bool
		wantArtifact bool
		wantPrefix   string
	}{
		{KindBare, false, false, ""},
		{KindSourceLive, true, false, "live+"},
		{KindSourcePull, true, false, "pull+"},
		{KindArtifactVOD, false, true, "vod+"},
		{KindArtifactDVR, false, true, "dvr+"},
		{KindArtifactProcessing, false, true, "processing+"},
	}
	for _, tc := range cases {
		t.Run(tc.k.String(), func(t *testing.T) {
			if got := tc.k.IsSource(); got != tc.wantSource {
				t.Errorf("IsSource() = %v, want %v", got, tc.wantSource)
			}
			if got := tc.k.IsArtifact(); got != tc.wantArtifact {
				t.Errorf("IsArtifact() = %v, want %v", got, tc.wantArtifact)
			}
			if got := tc.k.Prefix(); got != tc.wantPrefix {
				t.Errorf("Prefix() = %q, want %q", got, tc.wantPrefix)
			}
		})
	}
}

func TestParsedShorthand(t *testing.T) {
	p := Parse("live+xyz")
	if !p.IsSource() || p.IsArtifact() {
		t.Errorf("live+xyz: IsSource=%v IsArtifact=%v", p.IsSource(), p.IsArtifact())
	}
	p = Parse("vod+xyz")
	if p.IsSource() || !p.IsArtifact() {
		t.Errorf("vod+xyz: IsSource=%v IsArtifact=%v", p.IsSource(), p.IsArtifact())
	}
	p = Parse("bare")
	if p.IsSource() || p.IsArtifact() {
		t.Errorf("bare: IsSource=%v IsArtifact=%v", p.IsSource(), p.IsArtifact())
	}
}

// All known prefixes round-trip: Parse(prefix+x).Kind.Prefix() == prefix.
func TestPrefixRoundtrip(t *testing.T) {
	for _, p := range prefixes {
		parsed := Parse(p.prefix + "x")
		if got := parsed.Kind.Prefix(); got != p.prefix {
			t.Errorf("Parse(%q+x).Kind.Prefix() = %q, want %q", p.prefix, got, p.prefix)
		}
	}
}
