package config

import "testing"

func TestGetStoragePathUsesDurableDefault(t *testing.T) {
	t.Setenv("HELMSMAN_STORAGE_LOCAL_PATH", "")

	if got := GetStoragePath(); got != "/var/lib/frameworks/edge-storage" {
		t.Fatalf("GetStoragePath() = %q, want durable edge storage default", got)
	}
}

func TestGetStoragePathUsesConfiguredPath(t *testing.T) {
	t.Setenv("HELMSMAN_STORAGE_LOCAL_PATH", "/srv/frameworks/storage")

	if got := GetStoragePath(); got != "/srv/frameworks/storage" {
		t.Fatalf("GetStoragePath() = %q, want configured path", got)
	}
}
