package mesh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	fwsops "frameworks/cli/pkg/sops"
)

// StagedFile is a file prepared to replace TargetPath, but not yet committed.
// Callers MUST call either Commit or Discard exactly once. If NoChange is
// true, the staged content matched the target's current content and Commit
// is a no-op; TempPath will be empty.
type StagedFile struct {
	TargetPath string
	TempPath   string
	NoChange   bool
}

// Commit replaces TargetPath with TempPath via os.Rename. Safe to call when
// NoChange is true (returns nil without touching the filesystem).
func (s *StagedFile) Commit() error {
	if s == nil || s.NoChange || s.TempPath == "" {
		return nil
	}
	if err := os.Rename(s.TempPath, s.TargetPath); err != nil {
		return fmt.Errorf("rename over %s: %w", s.TargetPath, err)
	}
	s.TempPath = ""
	return nil
}

// Discard removes the tempfile if it still exists. Safe to call after Commit
// or on a NoChange staging; never returns an error (removal failures are
// transient tempfile leaks, not correctness issues).
func (s *StagedFile) Discard() {
	if s == nil || s.TempPath == "" {
		return
	}
	_ = os.Remove(s.TempPath)
	s.TempPath = ""
}

// StageEncryptedYAML runs the canonical SOPS flow (decrypt → edit → re-encrypt)
// into a sibling tempfile next to path, without replacing the target. The
// returned StagedFile must be committed or discarded by the caller.
//
// If the source is not encrypted (fresh dev inventory), edit runs on plaintext
// and the re-encrypt step is skipped.
//
// ageKeyFile resolves the same way as fwsops.DecryptData.
func StageEncryptedYAML(ctx context.Context, path string, ageKeyFile string, edit func(plaintext []byte) ([]byte, error)) (*StagedFile, error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	encrypted := fwsops.IsEncryptedYAML(orig)
	var plaintext []byte
	if encrypted {
		plaintext, err = fwsops.DecryptData(orig, "yaml", ageKeyFile)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", path, err)
		}
	} else {
		plaintext = orig
	}

	updated, err := edit(plaintext)
	if err != nil {
		return nil, err
	}

	// Skip I/O entirely so repeated runs stay idempotent at the filesystem
	// level (no atime churn, no git-noise on sops metadata).
	if string(updated) == string(plaintext) {
		return &StagedFile{TargetPath: path, NoChange: true}, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".wg-edit-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("write temp %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("close temp %s: %w", tmpPath, err)
	}

	if encrypted {
		if err := fwsops.EncryptFileInPlace(ctx, tmpPath, fwsops.EncryptOptions{
			FilenameOverride: path,
			AgeKeyFile:       ageKeyFile,
		}); err != nil {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("sops encrypt: %w", err)
		}
	}

	return &StagedFile{TargetPath: path, TempPath: tmpPath}, nil
}

// EditEncryptedYAML is a convenience wrapper around StageEncryptedYAML that
// commits the staged file immediately. Preferred when the caller only needs
// to mutate a single file; for multi-file transactional commits, stage both
// targets and commit them in sequence.
func EditEncryptedYAML(ctx context.Context, path string, ageKeyFile string, edit func(plaintext []byte) ([]byte, error)) error {
	staged, err := StageEncryptedYAML(ctx, path, ageKeyFile, edit)
	if err != nil {
		return err
	}
	defer staged.Discard()
	return staged.Commit()
}

// CommitManifestAndHosts replaces manifestPath with the content at
// manifestTmpPath (via os.Rename) and then commits stagedHosts in that order.
// If the hosts commit fails, manifestPath is restored from manifestBackup via
// os.WriteFile. If the restore itself fails, the returned error names both
// paths and flags that manual recovery is required — the caller should surface
// it loudly.
//
// POSIX provides no atomic multi-file rename; this sequence is best-effort
// with rollback, not a true transaction. stagedHosts may be nil or in its
// NoChange state; both are treated as a no-op for the second commit.
func CommitManifestAndHosts(manifestPath, manifestTmpPath string, manifestBackup []byte, stagedHosts *StagedFile) error {
	if err := os.Rename(manifestTmpPath, manifestPath); err != nil {
		return fmt.Errorf("commit manifest %s: %w", manifestPath, err)
	}
	if err := stagedHosts.Commit(); err != nil {
		if restoreErr := os.WriteFile(manifestPath, manifestBackup, 0o644); restoreErr != nil {
			return fmt.Errorf("commit hosts failed (%w); ALSO failed to restore manifest at %s: %w — manual recovery required", err, manifestPath, restoreErr)
		}
		return fmt.Errorf("commit hosts failed, manifest rolled back to previous state: %w", err)
	}
	return nil
}
