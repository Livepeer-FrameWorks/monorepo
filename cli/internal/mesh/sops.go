package mesh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	fwsops "frameworks/cli/pkg/sops"
)

// EditEncryptedYAML decrypts a SOPS-encrypted YAML file, hands the plaintext
// bytes to edit, and re-encrypts the result in place. The flow matches the
// CLAUDE.md required sequence: decrypt → modify → encrypt → replace.
//
// If the source file is not encrypted (e.g. a fresh dev inventory), the edit
// runs against the plaintext and writeback skips the encrypt step.
//
// ageKeyFile resolves the same way as fwsops.DecryptData.
func EditEncryptedYAML(ctx context.Context, path string, ageKeyFile string, edit func(plaintext []byte) ([]byte, error)) error {
	orig, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	encrypted := fwsops.IsEncryptedYAML(orig)
	var plaintext []byte
	if encrypted {
		plaintext, err = fwsops.DecryptData(orig, "yaml", ageKeyFile)
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", path, err)
		}
	} else {
		plaintext = orig
	}

	updated, err := edit(plaintext)
	if err != nil {
		return err
	}

	// No change: skip I/O entirely so repeated runs stay idempotent at the
	// filesystem level (no atime churn, no git-noise on sops metadata).
	if string(updated) == string(plaintext) {
		return nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".wg-edit-*.yaml")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp %s: %w", tmpPath, err)
	}

	if encrypted {
		if err := sopsEncryptInPlace(ctx, tmpPath, ageKeyFile); err != nil {
			return fmt.Errorf("sops encrypt: %w", err)
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename over %s: %w", path, err)
	}
	return nil
}

// sopsEncryptInPlace shells out to `sops --encrypt --in-place`. The sops
// library exposes encrypt APIs but not the rule-matching path, so using the
// same binary the operator already has avoids a creator config mismatch.
func sopsEncryptInPlace(ctx context.Context, path, ageKeyFile string) error {
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops binary not found in PATH (install from https://github.com/getsops/sops): %w", err)
	}
	if ageKeyFile != "" {
		abs, err := filepath.Abs(ageKeyFile)
		if err != nil {
			return fmt.Errorf("resolve age key: %w", err)
		}
		prev := os.Getenv("SOPS_AGE_KEY_FILE")
		os.Setenv("SOPS_AGE_KEY_FILE", abs)
		defer os.Setenv("SOPS_AGE_KEY_FILE", prev)
	}
	cmd := exec.CommandContext(ctx, "sops", "--encrypt", "--in-place", path)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sops --encrypt: %w", err)
	}
	return nil
}
