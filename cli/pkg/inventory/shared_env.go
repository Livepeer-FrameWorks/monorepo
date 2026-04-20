package inventory

import (
	"fmt"
	"path/filepath"
	"strings"

	fwsops "frameworks/cli/pkg/sops"
)

// LoadSharedEnv reads, decrypts, and merges the manifest's top-level
// env_files into a single map. SOPS-encrypted files are decrypted using
// ageKey (empty falls back to SOPS_AGE_KEY_FILE, then the sops default).
// Absolute env_file paths are rejected; relative paths resolve against
// manifestDir. Later files override earlier keys.
//
// Does NOT validate or require any specific keys. Per-service env_file
// overrides are not handled here.
func LoadSharedEnv(manifest *Manifest, manifestDir, ageKey string) (map[string]string, error) {
	env := make(map[string]string)
	if manifest == nil {
		return env, nil
	}
	for _, envFile := range manifest.SharedEnvFiles() {
		if manifestDir != "" && filepath.IsAbs(envFile) {
			return nil, fmt.Errorf("env_files: absolute path %q is not allowed — use a relative path from the manifest directory", envFile)
		}
		envPath := envFile
		if manifestDir != "" && !filepath.IsAbs(envPath) {
			envPath = filepath.Join(manifestDir, envPath)
		}
		data, err := fwsops.DecryptFileIfEncrypted(envPath, ageKey)
		if err != nil {
			return nil, fmt.Errorf("env file %s: %w", envPath, err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			env[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return env, nil
}
