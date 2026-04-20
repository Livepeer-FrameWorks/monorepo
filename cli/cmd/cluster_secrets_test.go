package cmd

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestEffectiveGeoIPLicenseKey_FlagWins(t *testing.T) {
	shared := map[string]string{"MAXMIND_LICENSE_KEY": "from-gitops"}
	got := effectiveGeoIPLicenseKey(shared, "from-flag")
	if got != "from-flag" {
		t.Fatalf("explicit flag should win, got %q", got)
	}
}

func TestEffectiveGeoIPLicenseKey_SharedEnv(t *testing.T) {
	shared := map[string]string{"MAXMIND_LICENSE_KEY": "from-gitops"}
	got := effectiveGeoIPLicenseKey(shared, "")
	if got != "from-gitops" {
		t.Fatalf("should resolve from sharedEnv when flag empty, got %q", got)
	}
}

func TestEffectiveGeoIPLicenseKey_Empty(t *testing.T) {
	got := effectiveGeoIPLicenseKey(map[string]string{}, "")
	if got != "" {
		t.Fatalf("should be empty when both absent, got %q", got)
	}
	got = effectiveGeoIPLicenseKey(nil, "")
	if got != "" {
		t.Fatalf("nil sharedEnv with no flag should return empty, got %q", got)
	}
}

func TestResolveYugabytePassword_YamlWins(t *testing.T) {
	pg := &inventory.PostgresConfig{
		Engine:   "yugabyte",
		Password: "from-yaml",
	}
	shared := map[string]string{"DATABASE_PASSWORD": "from-gitops"}
	got, err := resolveYugabytePassword(pg, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-yaml" {
		t.Fatalf("pg.Password should win over sharedEnv, got %q", got)
	}
}

func TestResolveYugabytePassword_SharedEnvFallback(t *testing.T) {
	pg := &inventory.PostgresConfig{Engine: "yugabyte"}
	shared := map[string]string{"DATABASE_PASSWORD": "from-gitops"}
	got, err := resolveYugabytePassword(pg, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-gitops" {
		t.Fatalf("should fall back to DATABASE_PASSWORD, got %q", got)
	}
}

func TestResolveYugabytePassword_VanillaPostgres(t *testing.T) {
	pg := &inventory.PostgresConfig{
		Engine:   "postgres",
		Password: "ignored",
	}
	shared := map[string]string{"DATABASE_PASSWORD": "also-ignored"}
	got, err := resolveYugabytePassword(pg, shared)
	if err != nil {
		t.Fatalf("vanilla Postgres should not error, got %v", err)
	}
	if got != "" {
		t.Fatalf("vanilla Postgres uses peer auth, should return empty, got %q", got)
	}
}

func TestResolveYugabytePassword_YugabyteNoSecretErrors(t *testing.T) {
	pg := &inventory.PostgresConfig{Engine: "yugabyte"}
	_, err := resolveYugabytePassword(pg, map[string]string{})
	if err == nil {
		t.Fatal("expected error when Yugabyte has no password source (empty sharedEnv)")
	}
	if !strings.Contains(err.Error(), "env_files") {
		t.Fatalf("error should mention env_files, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "DATABASE_PASSWORD") {
		t.Fatalf("error should mention the canonical key, got %q", err.Error())
	}

	_, err = resolveYugabytePassword(pg, nil)
	if err == nil {
		t.Fatal("expected error when sharedEnv is nil and no yaml password")
	}
}

func TestResolveGeoIPMMDBPath_MaxMindMissingKeyMentionsEnvFiles(t *testing.T) {
	_, _, err := resolveGeoIPMMDBPath(context.TODO(), nil, "maxmind", "", "")
	if err == nil {
		t.Fatal("expected error when MaxMind source has no license key")
	}
	if !strings.Contains(err.Error(), "env_files") {
		t.Fatalf("error should mention env_files, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "MAXMIND_LICENSE_KEY") {
		t.Fatalf("error should mention the canonical key, got %q", err.Error())
	}
}
