package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func defaultEndpoints() Endpoints {
	return Endpoints{
		BridgeURL:             "http://localhost:18000",
		QuartermasterGRPCAddr: "localhost:19002",
		CommodoreGRPCAddr:     "localhost:19001",
		FoghornGRPCAddr:       "localhost:18019",
		DecklogGRPCAddr:       "localhost:18006",
		PeriscopeGRPCAddr:     "localhost:19004",
		PurserGRPCAddr:        "localhost:19003",
		SignalmanWSURL:        "ws://localhost:18009",
		SignalmanGRPCAddr:     "localhost:19005",
		NavigatorGRPCAddr:     "localhost:18011",
	}
}

// DefaultEndpoints returns the default endpoint set for local development.
// Used by the setup wizard as the starting point when prompting for endpoints.
func DefaultEndpoints() Endpoints { return defaultEndpoints() }

// configDirEnv holds the XDG environment variable name for CLI config.
const configDirEnv = "XDG_CONFIG_HOME"

// xdgConfigHome resolves $XDG_CONFIG_HOME, falling back to ~/.config per the
// XDG Base Directory Specification.
func xdgConfigHome() (string, error) {
	if v := strings.TrimSpace(os.Getenv(configDirEnv)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// canonicalConfigPath returns the XDG path for config.yaml. This is the
// only path the CLI reads or writes — no fallback to legacy locations.
func canonicalConfigPath() (string, error) {
	base, err := xdgConfigHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "frameworks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// ConfigPath returns the path config.yaml is read from and written to. It
// honors RuntimeOverrides.ConfigPath ahead of the canonical XDG default.
// The returned path is always valid; the file may or may not exist.
func ConfigPath() (string, error) {
	if o := GetRuntimeOverrides(); o.ConfigPathExplicit && o.ConfigPath != "" {
		return o.ConfigPath, nil
	}
	return canonicalConfigPath()
}

// Load reads the CLI config file. Returns an empty Config{} (and nil error)
// when the file is absent — callers decide whether missing config is
// acceptable via the strict/lax context resolvers. Only real I/O or parse
// failures surface as errors.
func Load() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Context{}
	}
	return cfg, nil
}

// Save writes the config to disk at the canonical path (or the --config
// override). Creates the parent directory if needed.
func Save(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return mkErr
	}
	b, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// IsLocalContext reports whether a context targets local development.
// Decided by inspecting the bridge URL — naming heuristics ("local",
// "default", "") were brittle: a fresh edge persona context named
// "default" should not silently dodge endpoint validation.
func IsLocalContext(ctx Context) bool {
	return IsLocalhostEndpoint(ctx.Endpoints.BridgeURL)
}

// IsLocalhostEndpoint returns true if the endpoint targets localhost.
func IsLocalhostEndpoint(endpoint string) bool {
	if endpoint == "" {
		return false
	}
	return strings.HasPrefix(endpoint, "localhost") ||
		strings.HasPrefix(endpoint, "127.0.0.1") ||
		strings.Contains(endpoint, "://localhost") ||
		strings.Contains(endpoint, "://127.0.0.1")
}

// RequireEndpoint validates that an endpoint is configured for non-local
// contexts. Returns the endpoint if valid, or a descriptive error.
func RequireEndpoint(ctx Context, endpointName, endpoint string, allowLocalhost bool) (string, error) {
	if endpoint == "" {
		if IsLocalContext(ctx) {
			return "", fmt.Errorf("%s not configured; run 'frameworks setup' to configure endpoints", endpointName)
		}
		return "", fmt.Errorf("%s not configured for context %q; set it with 'frameworks context set-url'", endpointName, ctx.Name)
	}
	if !allowLocalhost && !IsLocalContext(ctx) && IsLocalhostEndpoint(endpoint) {
		return "", fmt.Errorf("%s is localhost but context %q is not local; this is likely misconfigured", endpointName, ctx.Name)
	}
	return endpoint, nil
}
