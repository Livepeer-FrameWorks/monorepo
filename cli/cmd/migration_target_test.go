package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestResolveMigrationTarget_ExplicitConcrete(t *testing.T) {
	got, err := resolveMigrationTargetFromParts(&inventory.Manifest{Channel: "stable"}, nil, "v0.5.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "v0.5.0" {
		t.Errorf("got %q, want v0.5.0", got)
	}
}

func TestResolveMigrationTarget_ExplicitChannelRejected(t *testing.T) {
	_, err := resolveMigrationTargetFromParts(&inventory.Manifest{Channel: "stable"}, nil, "stable")
	if err == nil {
		t.Fatal("want error for channel name as explicit version, got nil")
	}
	if !strings.Contains(err.Error(), "concrete vX.Y.Z") {
		t.Errorf("error message missing hint: %v", err)
	}
}

func TestResolveMigrationTarget_ExplicitGarbageRejected(t *testing.T) {
	_, err := resolveMigrationTargetFromParts(&inventory.Manifest{Channel: "stable"}, nil, "1.2.3")
	if err == nil {
		t.Fatal("want error for missing v prefix, got nil")
	}
}

func TestResolveMigrationTarget_VersionWithRCSuffix(t *testing.T) {
	got, err := resolveMigrationTargetFromParts(&inventory.Manifest{}, nil, "v0.5.0-rc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "v0.5.0-rc1" {
		t.Errorf("got %q, want v0.5.0-rc1", got)
	}
}
