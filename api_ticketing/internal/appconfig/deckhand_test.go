package appconfig

import (
	"errors"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadDeckhand(values map[string]string) (*Deckhand, error) {
	return config.Load[Deckhand](config.Options{
		Service: "deckhand",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
}

func requiredDeckhand() map[string]string {
	return map[string]string{"SERVICE_TOKEN": "token", "CHATWOOT_API_TOKEN": "chatwoot"}
}

func TestDeckhandDefaults(t *testing.T) {
	cfg, err := loadDeckhand(requiredDeckhand())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "18015" || cfg.GRPCPort != "19006" {
		t.Fatalf("ports = http %q grpc %q", cfg.HTTPListenPort(), cfg.GRPCPort)
	}
	if cfg.ChatwootBaseURL() != "http://chatwoot:3000" || cfg.ChatwootAccountID != 1 || cfg.ChatwootInboxID != 1 {
		t.Fatalf("chatwoot = %q account %d inbox %d", cfg.ChatwootBaseURL(), cfg.ChatwootAccountID, cfg.ChatwootInboxID)
	}
	if cfg.WebhookRateLimitPerMin != 600 || cfg.JWTSecret != "" {
		t.Fatalf("rate limit %d jwt-set %v", cfg.WebhookRateLimitPerMin, cfg.JWTSecret != "")
	}
}

func TestDeckhandRequiresServiceAndChatwootTokens(t *testing.T) {
	_, err := loadDeckhand(nil)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	for _, key := range []string{"SERVICE_TOKEN", "CHATWOOT_API_TOKEN"} {
		if !slices.Contains(loadErr.Missing, key) {
			t.Fatalf("missing = %v, want %s", loadErr.Missing, key)
		}
	}
}

func TestDeckhandLegacyPortOverridesHTTPPort(t *testing.T) {
	values := requiredDeckhand()
	values["DECKHAND_PORT"] = "28015"
	cfg, err := loadDeckhand(values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "28015" {
		t.Fatalf("DECKHAND_PORT = %q", cfg.HTTPListenPort())
	}
	values["PORT"] = "38015"
	if cfg, err = loadDeckhand(values); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "38015" {
		t.Fatalf("PORT override = %q", cfg.HTTPListenPort())
	}
}
