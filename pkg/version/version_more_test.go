package version

import (
	"errors"
	"testing"
)

type failingWriter struct {
	failAfter int
	writes    int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.failAfter {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestGetShortCommitBoundary(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	tests := []struct {
		name   string
		commit string
		want   string
	}{
		{"len6", "abcdef", "abcdef"},
		{"len7", "abcdefg", "abcdefg"},
		{"len8", "abcdefgh", "abcdefg"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			GitCommit = tt.commit
			if got := GetShortCommit(); got != tt.want {
				t.Fatalf("GetShortCommit(%q)=%q want %q", tt.commit, got, tt.want)
			}
		})
	}
}

func TestHandleCommandJSONEncodeError(t *testing.T) {
	w := &failingWriter{failAfter: 0}
	handled, err := HandleCommand([]string{"version", "--json"}, w)
	if !handled {
		t.Fatal("expected handled=true")
	}
	if err == nil {
		t.Fatal("expected error from failing writer on json encode")
	}
}

func TestHandleCommandTextWriteErrors(t *testing.T) {
	old := GetInfo()
	ComponentName = "svc"
	t.Cleanup(func() {
		ComponentName = old.ComponentName
	})

	for failAfter := 0; failAfter < 4; failAfter++ {
		w := &failingWriter{failAfter: failAfter}
		handled, err := HandleCommand([]string{"version"}, w)
		if !handled {
			t.Fatalf("failAfter=%d: expected handled=true", failAfter)
		}
		if err == nil {
			t.Fatalf("failAfter=%d: expected error from failing writer", failAfter)
		}
	}
}
