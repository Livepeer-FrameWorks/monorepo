package cmd

import "testing"

func TestNormalizeReleaseTargetVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{" latest ", ""},
		{"LATEST", ""},
		{"v1.2.3", "v1.2.3"},
	}

	for _, tc := range tests {
		if got := normalizeReleaseTargetVersion(tc.input); got != tc.want {
			t.Fatalf("normalizeReleaseTargetVersion(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeReleaseTargetChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{" stable ", "stable"},
		{"STABLE", "stable"},
		{"Rc", "rc"},
	}

	for _, tc := range tests {
		got, err := normalizeReleaseTargetChannel(tc.input)
		if err != nil {
			t.Fatalf("normalizeReleaseTargetChannel(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeReleaseTargetChannel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeReleaseTargetChannelRejectsUnknown(t *testing.T) {
	t.Parallel()

	if _, err := normalizeReleaseTargetChannel("nightly"); err == nil {
		t.Fatal("normalizeReleaseTargetChannel accepted nightly; want error")
	}
}

func TestNormalizeReleaseTargetChannelRejectsEdgeAsTrack(t *testing.T) {
	t.Parallel()

	if _, err := normalizeReleaseTargetChannel("edge"); err == nil {
		t.Fatal("normalizeReleaseTargetChannel accepted edge as a release track; want error")
	}
}

func TestEdgeReleaseHasUpdateableComponent(t *testing.T) {
	t.Parallel()

	if edgeReleaseHasUpdateableComponent(map[string]edgeReleaseComponentSpec{"config_schema": {Version: "4"}}) {
		t.Fatal("config_schema-only release should not be updateable")
	}
	if !edgeReleaseHasUpdateableComponent(map[string]edgeReleaseComponentSpec{"mist": {Version: "v1.2.3"}}) {
		t.Fatal("mist release should be updateable")
	}
}

func TestPlatformKeyFromArtifactName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"linux-amd64":   "linux/amd64",
		"darwin/arm64":  "darwin/arm64",
		" LINUX-ARM64 ": "linux/arm64",
	}
	for input, want := range tests {
		got, ok := platformKeyFromArtifactName(input)
		if !ok {
			t.Fatalf("platformKeyFromArtifactName(%q) returned ok=false", input)
		}
		if got != want {
			t.Fatalf("platformKeyFromArtifactName(%q) = %q, want %q", input, got, want)
		}
	}
}
