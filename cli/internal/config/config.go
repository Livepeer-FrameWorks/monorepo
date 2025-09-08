package config

import (
	"errors"
	"gopkg.in/yaml.v3"
	"io/fs"
	"os"
	"path/filepath"
)

func defaultEndpoints() Endpoints {
	return Endpoints{
		GatewayURL:         "http://localhost:18000",
		QuartermasterURL:   "http://localhost:18002",
		ControlURL:         "http://localhost:18001",
		FoghornHTTPURL:     "http://localhost:18008",
		FoghornGRPCAddr:    "localhost:18019",
		DecklogGRPCAddr:    "localhost:18006",
		PeriscopeQueryURL:  "http://localhost:18004",
		PeriscopeIngestURL: "http://localhost:18005",
		PurserURL:          "http://localhost:18003",
		SignalmanWSURL:     "ws://localhost:18009",
	}
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
