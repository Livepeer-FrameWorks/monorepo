package config

import (
	"testing"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
)

func TestGetStoragePathUsesDurableDefault(t *testing.T) {
	appconfigtest.Setenv(t, "HELMSMAN_STORAGE_LOCAL_PATH", "")

	if got := GetStoragePath(); got != "/var/lib/frameworks/edge-storage" {
		t.Fatalf("GetStoragePath() = %q, want durable edge storage default", got)
	}
}

func TestGetStoragePathUsesConfiguredPath(t *testing.T) {
	appconfigtest.Setenv(t, "HELMSMAN_STORAGE_LOCAL_PATH", "/srv/frameworks/storage")

	if got := GetStoragePath(); got != "/srv/frameworks/storage" {
		t.Fatalf("GetStoragePath() = %q, want configured path", got)
	}
}
