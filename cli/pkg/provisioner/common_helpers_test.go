package provisioner

import (
	"reflect"
	"testing"
)

// isReleaseChannel decides whether a configured version is a moving channel
// (which must be resolved against the manifest) versus an already-pinned
// version. The empty string counts as a channel (use the default).
func TestIsReleaseChannel(t *testing.T) {
	channels := []string{"", "stable", "latest"}
	for _, v := range channels {
		if !isReleaseChannel(v) {
			t.Errorf("isReleaseChannel(%q) = false, want true", v)
		}
	}
	pinned := []string{"v1.2.3", "1.2.3", "STABLE", "main"}
	for _, v := range pinned {
		if isReleaseChannel(v) {
			t.Errorf("isReleaseChannel(%q) = true, want false", v)
		}
	}
}

// releaseVersion chooses the manifest-resolved version only when the configured
// value was a channel; an explicitly pinned version is always preserved.
func TestReleaseVersion(t *testing.T) {
	if got := releaseVersion("stable", "v1.4.0"); got != "v1.4.0" {
		t.Errorf("channel configured: got %q want resolved v1.4.0", got)
	}
	if got := releaseVersion("", "v1.4.0"); got != "v1.4.0" {
		t.Errorf("empty configured: got %q want resolved v1.4.0", got)
	}
	if got := releaseVersion("v1.2.3", "v1.4.0"); got != "v1.2.3" {
		t.Errorf("pinned configured: got %q want pinned v1.2.3", got)
	}
}

func TestPlatformChannelFromMetadata(t *testing.T) {
	if got := platformChannelFromMetadata(map[string]any{"platform_channel": "edge"}); got != "edge" {
		t.Errorf("present: got %q", got)
	}
	if got := platformChannelFromMetadata(map[string]any{}); got != "" {
		t.Errorf("absent: got %q want empty", got)
	}
	// Wrong dynamic type falls back to empty (use default channel).
	if got := platformChannelFromMetadata(map[string]any{"platform_channel": 5}); got != "" {
		t.Errorf("wrong type: got %q want empty", got)
	}
}

func TestDbSet(t *testing.T) {
	got := dbSet([]string{"a", "b", "a"})
	want := map[string]struct{}{"a": {}, "b": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dbSet = %#v, want %#v", got, want)
	}
	if _, ok := got["c"]; ok {
		t.Fatalf("dbSet reported membership for absent key")
	}
}

func TestOrElse(t *testing.T) {
	if got := orElse("v", "fallback"); got != "v" {
		t.Errorf("non-empty: got %q", got)
	}
	if got := orElse("", "fallback"); got != "fallback" {
		t.Errorf("empty: got %q want fallback", got)
	}
}
