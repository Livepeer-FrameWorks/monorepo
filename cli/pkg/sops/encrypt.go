package sops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EncryptOptions controls how a plaintext file is encrypted by the sops CLI.
type EncryptOptions struct {
	// FilenameOverride is the real target path SOPS should use for config-rule
	// lookup and format inference when encrypting a staged tempfile.
	FilenameOverride string
	AgeKeyFile       string
}

// EncryptFileInPlace shells out to `sops --encrypt --in-place`.
//
// The SOPS library decrypt API accepts raw bytes, but encryption still needs
// creation-rule matching from .sops.yaml. Calling the local sops binary keeps
// CLI behavior aligned with operator workstations and CI.
func EncryptFileInPlace(ctx context.Context, path string, opts EncryptOptions) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops binary not found in PATH (install from https://github.com/getsops/sops): %w", err)
	}

	env := os.Environ()
	if opts.AgeKeyFile != "" {
		abs, err := resolveAgeKeyFile(opts.AgeKeyFile)
		if err != nil {
			return err
		}
		env = envWith(env, "SOPS_AGE_KEY_FILE", abs)
	}

	args := []string{}
	configLookupPath := path
	if opts.FilenameOverride != "" {
		configLookupPath = opts.FilenameOverride
	}
	if configPath, ok := findConfigFile(configLookupPath); ok {
		args = append(args, "--config", configPath)
	}
	args = append(args, "--encrypt", "--in-place")
	if opts.FilenameOverride != "" {
		args = append(args, "--filename-override", opts.FilenameOverride)
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, "sops", args...)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sops --encrypt: %w", err)
	}
	return nil
}

func envWith(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func findConfigFile(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	dir := path
	if ext := filepath.Ext(path); ext != "" {
		dir = filepath.Dir(path)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(abs, ".sops.yaml")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}
