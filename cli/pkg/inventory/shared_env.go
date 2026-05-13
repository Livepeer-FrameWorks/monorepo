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
	if manifest == nil {
		return map[string]string{}, nil
	}
	return mergeEnvFilesInto(map[string]string{}, manifest.SharedEnvFiles(), manifestDir, ageKey, "env_files")
}

// LoadClusterEnvs reads, decrypts, and merges each ClusterConfig.EnvFiles
// list into a per-cluster env map keyed by cluster ID. SOPS-encrypted files
// are decrypted using ageKey. Absolute paths are rejected; relative paths
// resolve against manifestDir. Later files override earlier keys within a
// cluster's list, but per-cluster envs never spill across clusters — each
// (service, cluster) replica picks up only its own cluster's env at render
// time. Clusters with no env_files entries are omitted from the result.
func LoadClusterEnvs(manifest *Manifest, manifestDir, ageKey string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	if manifest == nil {
		return out, nil
	}
	for clusterID, cluster := range manifest.Clusters {
		if len(cluster.EnvFiles) == 0 {
			continue
		}
		env, err := mergeEnvFilesInto(map[string]string{}, cluster.EnvFiles, manifestDir, ageKey, fmt.Sprintf("clusters.%s.env_files", clusterID))
		if err != nil {
			return nil, err
		}
		out[clusterID] = env
	}
	return out, nil
}

// mergeEnvFilesInto resolves each env file (relative to manifestDir, SOPS-
// decrypting where needed) and merges KEY=VALUE pairs into target. label
// names the manifest field for error messages.
func mergeEnvFilesInto(target map[string]string, envFiles []string, manifestDir, ageKey, label string) (map[string]string, error) {
	for _, envFile := range envFiles {
		envFile = strings.TrimSpace(envFile)
		if envFile == "" {
			continue
		}
		if manifestDir != "" && filepath.IsAbs(envFile) {
			return nil, fmt.Errorf("%s: absolute path %q is not allowed — use a relative path from the manifest directory", label, envFile)
		}
		envPath := envFile
		if manifestDir != "" && !filepath.IsAbs(envPath) {
			envPath = filepath.Join(manifestDir, envPath)
		}
		data, err := fwsops.DecryptFileIfEncrypted(envPath, ageKey)
		if err != nil {
			return nil, fmt.Errorf("%s: env file %s: %w", label, envPath, err)
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
			target[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return target, nil
}

// ResolveSharedEnvPlaceholder resolves a whole-string ${KEY} placeholder from
// a previously loaded shared env map. It intentionally does not perform global
// string interpolation; callers opt into this only for fields that are allowed
// to reference shared env secrets.
func ResolveSharedEnvPlaceholder(value string, sharedEnv map[string]string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return value, nil
	}
	key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}"))
	if key == "" {
		return "", fmt.Errorf("empty shared env placeholder %q", value)
	}
	resolved := strings.TrimSpace(sharedEnv[key])
	if resolved == "" {
		return "", fmt.Errorf("shared env placeholder %s is not set", key)
	}
	return resolved, nil
}
