package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// sha256 of the literal "hello".
const helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestCalculateFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, size, err := calculateFileSHA256(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if hash != helloSHA256 {
		t.Errorf("hash = %s, want %s", hash, helloSHA256)
	}

	if _, _, err := calculateFileSHA256(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestBackupManifestRoundTripAndTamperDetection(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sql")
	if err := os.WriteFile(dataPath, []byte("BACKUP DATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, size, err := calculateFileSHA256(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]BackupFile{
		"data": {Path: dataPath, Size: size, SHA256: hash, Component: "postgres"},
	}

	const ts = "20260608T120000Z"
	if err := writeBackupManifest(dir, ts, "postgres", files); err != nil {
		t.Fatalf("writeBackupManifest: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest-"+ts+".json")

	t.Run("intact_backup_verifies_clean", func(t *testing.T) {
		errs, err := VerifyBackupManifest(manifestPath)
		if err != nil {
			t.Fatalf("VerifyBackupManifest: %v", err)
		}
		if len(errs) != 0 {
			t.Fatalf("expected no verification errors, got %v", errs)
		}
	})

	t.Run("tampered_file_is_flagged", func(t *testing.T) {
		// Rewrite to a different length so both size and checksum diverge.
		if err := os.WriteFile(dataPath, []byte("TAMPERED CONTENT!!"), 0o644); err != nil {
			t.Fatal(err)
		}
		errs, err := VerifyBackupManifest(manifestPath)
		if err != nil {
			t.Fatalf("VerifyBackupManifest: %v", err)
		}
		if len(errs) == 0 {
			t.Fatal("expected verification errors for tampered file")
		}
	})

	t.Run("missing_manifest_errors", func(t *testing.T) {
		if _, err := VerifyBackupManifest(filepath.Join(dir, "nope.json")); err == nil {
			t.Error("expected error for missing manifest")
		}
	})
}
