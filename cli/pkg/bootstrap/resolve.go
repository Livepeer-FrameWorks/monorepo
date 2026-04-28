package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/sops"

	"gopkg.in/yaml.v3"
)

// Resolver turns SecretRef instances into plaintext values at render time. Production
// callers use DefaultResolver, which supports the four ref shapes documented in the
// schema (sops, env, file, flag). Tests can stub Resolver to inject literal values.
type Resolver interface {
	Resolve(SecretRef) (string, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(SecretRef) (string, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(ref SecretRef) (string, error) { return f(ref) }

// DefaultResolver supports the four SecretRef forms.
//   - sops:  read SOPS-encrypted file at SOPS path (relative to BaseDir or absolute),
//     decrypt via the configured age key, return the value at Key.
//   - env:   read os.Getenv(name).
//   - file:  read the file contents at path (mode is the operator's responsibility).
//   - flag:  look up the flag name in Flags. Flags is what the CLI populates from
//     cobra at render time.
type DefaultResolver struct {
	// BaseDir is the directory SOPS paths are resolved relative to (typically the
	// gitops repo root). Empty means CWD.
	BaseDir string
	// AgeKeyFile is the SOPS age key path. Empty means $SOPS_AGE_KEY_FILE.
	AgeKeyFile string
	// Flags maps CLI flag names to their values (e.g. "bootstrap-admin-password" →
	// "<password>"). Populated by the caller before Render.
	Flags map[string]string
}

// Resolve implements Resolver.
func (r *DefaultResolver) Resolve(ref SecretRef) (string, error) {
	if ref.IsZero() {
		return "", errors.New("empty SecretRef")
	}

	set := 0
	if ref.SOPS != "" || ref.Key != "" {
		set++
	}
	if ref.Env != "" {
		set++
	}
	if ref.File != "" {
		set++
	}
	if ref.Flag != "" {
		set++
	}
	if set != 1 {
		return "", fmt.Errorf("SecretRef must set exactly one of {sops+key, env, file, flag}; got %+v", ref)
	}

	switch {
	case ref.SOPS != "":
		if ref.Key == "" {
			return "", errors.New("SecretRef sops requires key")
		}
		return r.resolveSOPS(ref.SOPS, ref.Key)
	case ref.Env != "":
		return resolveEnv(ref.Env)
	case ref.File != "":
		return r.resolveFile(ref.File)
	case ref.Flag != "":
		return r.resolveFlag(ref.Flag)
	}
	return "", fmt.Errorf("unrecognized SecretRef shape: %+v", ref)
}

func (r *DefaultResolver) resolveSOPS(path, key string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) && r.BaseDir != "" {
		abs = filepath.Join(r.BaseDir, path)
	}
	plain, err := sops.DecryptFileIfEncrypted(abs, r.AgeKeyFile)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", path, err)
	}
	value, ok := lookupKey(plain, sops.FormatFromPath(abs), key)
	if !ok {
		return "", fmt.Errorf("key %q not found in %s", key, path)
	}
	return value, nil
}

func resolveEnv(name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %q not set", name)
	}
	return v, nil
}

func (r *DefaultResolver) resolveFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func (r *DefaultResolver) resolveFlag(name string) (string, error) {
	if r.Flags == nil {
		return "", fmt.Errorf("flag %q not provided to resolver", name)
	}
	v, ok := r.Flags[name]
	if !ok || v == "" {
		return "", fmt.Errorf("flag %q not provided", name)
	}
	return v, nil
}

// lookupKey extracts a key from decrypted SOPS file contents.
//
//   - env: dotenv-style `KEY=value` lines, comments and quotes handled.
//   - yaml: yaml.v3 decodes the document into a top-level mapping; key is the
//     map key. Nested structures are rejected — the schema only references
//     top-level keys, and ad-hoc traversal is exactly what got us into trouble.
func lookupKey(data []byte, format, key string) (string, bool) {
	switch format {
	case "env":
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				continue
			}
			k := strings.TrimSpace(line[:eq])
			if k != key {
				continue
			}
			v := strings.TrimSpace(line[eq+1:])
			v = strings.Trim(v, `"'`)
			return v, true
		}
		return "", false
	case "yaml":
		var top map[string]any
		if err := yaml.Unmarshal(data, &top); err != nil {
			return "", false
		}
		raw, ok := top[key]
		if !ok {
			return "", false
		}
		// Only scalar values are supported; secrets are not nested objects.
		switch v := raw.(type) {
		case string:
			return v, true
		case int, int64, float64, bool:
			return fmt.Sprintf("%v", v), true
		default:
			return "", false
		}
	default:
		return "", false
	}
}
