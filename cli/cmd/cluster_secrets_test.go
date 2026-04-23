package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	_, _, err := resolveGeoIPMMDBPath(context.TODO(), "maxmind", "", "")
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

func TestGeoIPCacheFresh_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	fresh, err := geoIPCacheFresh(path, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh {
		t.Fatal("missing cache file should not be fresh")
	}
}

func TestGeoIPCacheFresh_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write empty cache file: %v", err)
	}
	fresh, err := geoIPCacheFresh(path, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh {
		t.Fatal("empty cache file should not be fresh")
	}
}

func TestGeoIPCacheFresh_RecentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-City.mmdb")
	now := time.Now()
	if err := os.WriteFile(path, []byte("mmdb"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	recent := now.Add(-2 * time.Hour)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatalf("set file times: %v", err)
	}
	fresh, err := geoIPCacheFresh(path, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fresh {
		t.Fatal("recent cache file should be fresh")
	}
}

func TestGeoIPCacheFresh_StaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-City.mmdb")
	now := time.Now()
	if err := os.WriteFile(path, []byte("mmdb"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	stale := now.Add(-(geoIPCacheTTL + time.Hour))
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("set file times: %v", err)
	}
	fresh, err := geoIPCacheFresh(path, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh {
		t.Fatal("stale cache file should not be fresh")
	}
}
