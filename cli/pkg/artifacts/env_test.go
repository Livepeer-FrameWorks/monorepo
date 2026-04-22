package artifacts

import (
	"reflect"
	"testing"
)

func TestParseEnvBytes_flexibleSortedFormat(t *testing.T) {
	t.Parallel()
	// Matches cli/pkg/provisioner/flexible.go writeServiceEnvFile output.
	input := []byte("BAR=2\nFOO=1\nNESTED=a=b=c\n")
	got := ParseEnvBytes(input)
	want := map[string]string{"BAR": "2", "FOO": "1", "NESTED": "a=b=c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseEnvBytes_templatesCommentHeaderFormat(t *testing.T) {
	t.Parallel()
	// Matches cli/pkg/provisioner/templates.go GenerateEnvFile output.
	input := []byte("# Environment for bridge\nSERVICE_NAME=bridge\n\nFOO=1\nBAR=2\n")
	got := ParseEnvBytes(input)
	want := map[string]string{"SERVICE_NAME": "bridge", "FOO": "1", "BAR": "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseEnvBytes_bothFormatsProduceSameMapGivenSameKeys(t *testing.T) {
	t.Parallel()
	// A file that has the same key set in either format must round-trip
	// to an identical map, so key-level diff is emitter-agnostic.
	sorted := []byte("BAR=2\nFOO=1\n")
	commented := []byte("# header\nBAR=2\nFOO=1\n")
	if !reflect.DeepEqual(ParseEnvBytes(sorted), ParseEnvBytes(commented)) {
		t.Errorf("same-key files produced different maps: %v vs %v",
			ParseEnvBytes(sorted), ParseEnvBytes(commented))
	}
}

func TestParseEnvBytes_skipsBlankAndCommentLines(t *testing.T) {
	t.Parallel()
	input := []byte("\n  \n# comment\nFOO=1\n   # indented comment\n")
	got := ParseEnvBytes(input)
	if !reflect.DeepEqual(got, map[string]string{"FOO": "1"}) {
		t.Errorf("unexpected: %v", got)
	}
}

func TestParseEnvBytes_trimsWhitespace(t *testing.T) {
	t.Parallel()
	input := []byte("  FOO = 1  \n  BAR=  2  \n")
	got := ParseEnvBytes(input)
	want := map[string]string{"FOO": "1", "BAR": "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseEnvBytes_lastWinsOnDuplicateKey(t *testing.T) {
	t.Parallel()
	input := []byte("FOO=1\nFOO=2\n")
	got := ParseEnvBytes(input)
	if got["FOO"] != "2" {
		t.Errorf("expected last-wins FOO=2, got %q", got["FOO"])
	}
}

func TestParseEnvBytes_emptyInputReturnsEmptyMap(t *testing.T) {
	t.Parallel()
	if got := ParseEnvBytes(nil); len(got) != 0 {
		t.Errorf("nil input: want empty map, got %v", got)
	}
	if got := ParseEnvBytes([]byte{}); len(got) != 0 {
		t.Errorf("empty input: want empty map, got %v", got)
	}
}

func TestParseEnvBytes_skipsBareKeysWithoutEquals(t *testing.T) {
	t.Parallel()
	input := []byte("NOT_AN_ENTRY\nFOO=1\n")
	got := ParseEnvBytes(input)
	if !reflect.DeepEqual(got, map[string]string{"FOO": "1"}) {
		t.Errorf("unexpected: %v", got)
	}
}
