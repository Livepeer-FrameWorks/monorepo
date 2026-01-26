package config

import (
	"bufio"
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
		GatewayURL:            "http://localhost:18000",
		QuartermasterURL:      "http://localhost:18002", // deprecated
		QuartermasterGRPCAddr: "localhost:19002",
		ControlURL:            "http://localhost:18001", // deprecated
		CommodoreGRPCAddr:     "localhost:19001",
		FoghornHTTPURL:        "http://localhost:18008", // deprecated
		FoghornGRPCAddr:       "localhost:18019",
		DecklogGRPCAddr:       "localhost:18006",
		PeriscopeQueryURL:     "http://localhost:18004", // deprecated
		PeriscopeGRPCAddr:     "localhost:19004",
		PeriscopeIngestURL:    "http://localhost:18005", // deprecated
		PurserURL:             "http://localhost:18003", // deprecated
		PurserGRPCAddr:        "localhost:19003",
		SignalmanWSURL:        "ws://localhost:18009",
		SignalmanGRPCAddr:     "localhost:19005",
		NavigatorGRPCAddr:     "localhost:18011", // Navigator (DNS/certs) gRPC
	}
}

// DefaultEndpoints returns the default endpoint set for local development.
func DefaultEndpoints() Endpoints {
	return defaultEndpoints()
}

func defaultContext() Context {
	return Context{
		Name:      "local",
		Endpoints: defaultEndpoints(),
		Executor:  Executor{Type: "local"},
	}
}

func defaultConfig() Config {
	ctx := defaultContext()
	return Config{
		Current:  ctx.Name,
		Contexts: map[string]Context{ctx.Name: ctx},
	}
}

func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".frameworks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

func Load() (Config, string, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, "", err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg := defaultConfig()
		return cfg, path, nil
	}
	if err != nil {
		return Config{}, path, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, path, err
	}
	if cfg.Contexts == nil || cfg.Current == "" { // minimal fixup
		d := defaultConfig()
		if cfg.Contexts == nil {
			cfg.Contexts = d.Contexts
		}
		if cfg.Current == "" {
			cfg.Current = d.Current
		}
	}
	return cfg, path, nil
}

func Save(cfg Config, path string) error {
	if path == "" {
		var err error
		path, err = ConfigPath()
		if err != nil {
			return err
		}
	}
	b, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func GetCurrent(cfg Config) Context {
	if c, ok := cfg.Contexts[cfg.Current]; ok {
		return c
	}
	return defaultContext()
}

// ResolveAuth merges context auth with ~/.frameworks/.env and OS env values.
// SERVICE_TOKEN is preferred, then FW_API_TOKEN as a fallback for service auth.
func ResolveAuth(ctx Context) Auth {
	auth := ctx.Auth
	envMap, _ := LoadEnvFile()
	if auth.ServiceToken == "" {
		if v := GetEnvValue("SERVICE_TOKEN", envMap); v != "" {
			auth.ServiceToken = v
		} else if v := GetEnvValue("FW_API_TOKEN", envMap); v != "" {
			auth.ServiceToken = v
		}
	}
	if auth.JWT == "" {
		if v := GetEnvValue("FW_JWT", envMap); v != "" {
			auth.JWT = v
		}
	}
	return auth
}

// LoadEnvFile loads environment variables from a .env file.
// It looks for .env in the current directory first, then in ~/.frameworks/.env
// Returns a map of key=value pairs. Does not modify os.Environ().
func LoadEnvFile() (map[string]string, error) {
	envMap := make(map[string]string)

	// Try current directory first
	paths := []string{".env"}

	// Then try ~/.frameworks/.env
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".frameworks", ".env"))
	}

	for _, path := range paths {
		if err := loadEnvFileInto(path, envMap); err == nil {
			return envMap, nil
		}
	}

	return envMap, nil // Return empty map if no .env found (not an error)
}

func loadEnvFileInto(path string, envMap map[string]string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=VALUE
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		envMap[key] = value
	}

	return scanner.Err()
}

// GetEnvValue returns an environment variable value, checking:
// 1. OS environment (os.Getenv)
// 2. Loaded .env file values
func GetEnvValue(key string, envMap map[string]string) string {
	// OS env takes precedence
	if v := os.Getenv(key); v != "" {
		return v
	}
	// Fall back to .env file
	if envMap != nil {
		return envMap[key]
	}
	return ""
}

// EnvFilePath returns the path to the .env file in ~/.frameworks/
func EnvFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".frameworks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, ".env"), nil
}

// SaveEnvValue saves or updates a key=value pair in ~/.frameworks/.env
// This preserves other values in the file.
func SaveEnvValue(key, value string) error {
	envPath, err := EnvFilePath()
	if err != nil {
		return err
	}

	// Load existing values
	envMap := make(map[string]string)
	_ = loadEnvFileInto(envPath, envMap) // Ignore error if file doesn't exist

	// Update the value
	envMap[key] = value

	// Write all values back
	return writeEnvFile(envPath, envMap)
}

// writeEnvFile writes all key=value pairs to the .env file
func writeEnvFile(path string, envMap map[string]string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	// Write header comment
	_, _ = file.WriteString("# FrameWorks CLI credentials\n")
	_, _ = file.WriteString("# Generated by 'frameworks login'\n\n")

	// Write each key=value
	for k, v := range envMap {
		// Quote values containing spaces or special characters
		if strings.ContainsAny(v, " \t\n\"'") {
			v = "\"" + strings.ReplaceAll(v, "\"", "\\\"") + "\""
		}
		if _, err := fmt.Fprintf(file, "%s=%s\n", k, v); err != nil {
			return err
		}
	}

	return nil
}

// IsLocalContext returns true if the context is "local" or "default"
func IsLocalContext(ctx Context) bool {
	return ctx.Name == "local" || ctx.Name == "default" || ctx.Name == ""
}

// IsLocalhostEndpoint returns true if the endpoint is localhost or 127.0.0.1
func IsLocalhostEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "localhost") ||
		strings.HasPrefix(endpoint, "127.0.0.1") ||
		strings.Contains(endpoint, "://localhost") ||
		strings.Contains(endpoint, "://127.0.0.1")
}

// RequireEndpoint validates that an endpoint is configured for non-local contexts.
// Returns the endpoint if valid, or an error if:
// - Endpoint is empty and context is not local
// - Endpoint is localhost and context is not local (unless allowLocalhost is true)
func RequireEndpoint(ctx Context, endpointName, endpoint string, allowLocalhost bool) (string, error) {
	if endpoint == "" {
		if IsLocalContext(ctx) {
			return "", fmt.Errorf("%s not configured; run 'frameworks context set' to configure endpoints", endpointName)
		}
		return "", fmt.Errorf("%s not configured for context %q; set it with 'frameworks context set'", endpointName, ctx.Name)
	}

	if !allowLocalhost && !IsLocalContext(ctx) && IsLocalhostEndpoint(endpoint) {
		return "", fmt.Errorf("%s is localhost but context %q is not local; this is likely misconfigured", endpointName, ctx.Name)
	}

	return endpoint, nil
}
