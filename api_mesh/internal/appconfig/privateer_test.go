package appconfig

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadPrivateer(env map[string]string) (*Privateer, error) {
	return config.Load[Privateer](config.Options{
		Service: "privateer",
		Lookup: func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		},
	})
}

func TestPrivateerDefaults(t *testing.T) {
	cfg, err := loadPrivateer(map[string]string{
		"SERVICE_TOKEN":         "token",
		"MESH_PRIVATE_KEY_FILE": "/etc/privateer/wg.key",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenHTTPPort() != "18012" {
		t.Fatalf("HTTP port = %q, want 18012", cfg.ListenHTTPPort())
	}
	if cfg.DNSPort != 53 || cfg.WireguardListenPort != 0 {
		t.Fatalf("ports = dns %d wireguard %d", cfg.DNSPort, cfg.WireguardListenPort)
	}
	if cfg.SyncInterval != 30*time.Second || cfg.SyncTimeout != 10*time.Second || cfg.CertSyncInterval != 5*time.Minute {
		t.Fatalf("intervals = %s/%s/%s", cfg.SyncInterval, cfg.SyncTimeout, cfg.CertSyncInterval)
	}
	if cfg.PKIDir != "/etc/frameworks/pki" || cfg.GRPCAllowInsecure || cfg.BootstrapInsecure {
		t.Fatalf("pki/insecure defaults = %q/%v/%v", cfg.PKIDir, cfg.GRPCAllowInsecure, cfg.BootstrapInsecure)
	}
}

func TestPrivateerPortPrecedence(t *testing.T) {
	cfg, err := loadPrivateer(map[string]string{
		"SERVICE_TOKEN":         "token",
		"MESH_PRIVATE_KEY_FILE": "/etc/privateer/wg.key",
		"PRIVATEER_PORT":        "28012",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenHTTPPort() != "28012" {
		t.Fatalf("PRIVATEER_PORT ignored: %q", cfg.ListenHTTPPort())
	}

	cfg, err = loadPrivateer(map[string]string{
		"SERVICE_TOKEN":         "token",
		"MESH_PRIVATE_KEY_FILE": "/etc/privateer/wg.key",
		"PRIVATEER_PORT":        "28012",
		"PORT":                  "38012",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenHTTPPort() != "38012" {
		t.Fatalf("PORT must take precedence over PRIVATEER_PORT, got %q", cfg.ListenHTTPPort())
	}
}

func TestPrivateerRequiresKeyFileAndServiceToken(t *testing.T) {
	_, err := loadPrivateer(map[string]string{})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	for _, key := range []string{"SERVICE_TOKEN", "MESH_PRIVATE_KEY_FILE"} {
		if !slices.Contains(loadErr.Missing, key) {
			t.Errorf("missing list %v does not name %s", loadErr.Missing, key)
		}
	}
}
