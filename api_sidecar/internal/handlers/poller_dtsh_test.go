package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidLocalDtshRemovesInvalidSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.mkv.dtsh")
	if err := os.WriteFile(path, []byte("not a dtsh"), 0o644); err != nil {
		t.Fatalf("write invalid dtsh: %v", err)
	}

	if validLocalDtsh(path) {
		t.Fatal("validLocalDtsh returned true for invalid sidecar")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid sidecar still exists, stat err=%v", err)
	}
}
