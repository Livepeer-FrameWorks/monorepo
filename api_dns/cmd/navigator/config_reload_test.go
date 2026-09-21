package main

import (
	"sync"
	"testing"

	"frameworks/api_dns/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func TestIssuanceSettingsFollowLiveReload(t *testing.T) {
	var mu sync.Mutex
	env := map[string]string{
		"NAVIGATOR_PORT":        "18010",
		"NAVIGATOR_GRPC_PORT":   "18011",
		"SERVICE_TOKEN":         "token",
		"FIELD_ENCRYPTION_KEY":  "field-key",
		"DATABASE_URL":          "postgres://navigator",
		"BRAND_DOMAIN":          "frameworks.network",
		"ACME_EMAIL":            "ops@frameworks.network",
		"CLOUDFLARE_API_TOKEN":  "cf-token",
		"CLOUDFLARE_ZONE_ID":    "zone",
		"CLOUDFLARE_ACCOUNT_ID": "account",
	}
	opts := config.Options{Service: "navigator", Lookup: func(key string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		value, ok := env[key]
		return value, ok
	}}
	cfg, err := config.Load[appconfig.Navigator](opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	live := config.NewLive(cfg, opts)
	source := func() string { return issuanceSettings(live.Get()).CAOrder }

	if got := source(); got != "" {
		t.Fatalf("initial CA order = %q", got)
	}

	mu.Lock()
	env["NAVIGATOR_ACME_CA_ORDER"] = "google-trust,letsencrypt"
	env["NAVIGATOR_CERT_ALLOWED_SUFFIXES"] = "example.org"
	mu.Unlock()
	if reloadErr := live.Reload(); reloadErr != nil {
		t.Fatalf("reload: %v", reloadErr)
	}
	settings := issuanceSettings(live.Get())
	if settings.CAOrder != "google-trust,letsencrypt" || settings.AllowedSuffixes != "example.org" {
		t.Fatalf("settings after reload = %+v", settings)
	}

	mu.Lock()
	delete(env, "ACME_EMAIL")
	env["NAVIGATOR_ACME_CA_ORDER"] = "letsencrypt"
	mu.Unlock()
	if reloadErr := live.Reload(); reloadErr == nil {
		t.Fatal("expected reload to fail without ACME_EMAIL")
	}
	if got := source(); got != "google-trust,letsencrypt" {
		t.Fatalf("failed reload replaced the snapshot: CA order %q", got)
	}
}

func TestInternalCAMaterialRequiresManagedInProduction(t *testing.T) {
	cfg := &appconfig.Navigator{BuildEnvironment: config.BuildEnvironment{BuildEnv: "production"}}
	if !internalCAMaterial(cfg).RequireManaged {
		t.Fatal("production must require managed internal CA material")
	}
	cfg.BuildEnv = ""
	if internalCAMaterial(cfg).RequireManaged {
		t.Fatal("development must allow an in-process internal CA")
	}
}
