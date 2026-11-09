package cmd

import (
	"bytes"
	"context"
	"io"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func TestPurserImmutableTaxRollbackBoundary(t *testing.T) {
	if !containsPurser(releases.RollbackDisabledFor("v0.3.14-rc4")) {
		t.Error("immutable invoice contract must disable automatic Purser rollback")
	}
	for _, tt := range []struct {
		current, target string
		refused         bool
	}{
		{"v0.3.14-rc4", "v0.3.14-rc3", true},
		{"v0.3.14-rc4", "v0.3.13", true},
		{"v0.3.14", "v0.3.14-rc4", false},
		{"v0.3.14-rc3", "v0.3.13", false},
	} {
		t.Run(tt.current+"_to_"+tt.target, func(t *testing.T) {
			err := sourceFloorRefusal([]runningInstance{{Service: "purser", Host: "billing-1", Version: tt.current}}, tt.target, releases.MinSourceVersionFor)
			if (err != nil) != tt.refused {
				t.Fatalf("refused=%t, want %t: %v", err != nil, tt.refused, err)
			}
		})
	}
	if err := sourceFloorRefusal([]runningInstance{{Service: "purser", Host: "old", Version: "v0.3.14-rc3"}, {Service: "purser", Host: "new", Version: "v0.3.14-rc4"}}, "v0.3.14-rc3", releases.MinSourceVersionFor); err == nil {
		t.Fatal("mixed replicas must protect the newer invoice contract")
	}
}

func TestPurserRollbackFenceRunsWithoutGenericSourceFloors(t *testing.T) {
	restore := stubSourceFloor(t, func() bool { return false }, releases.MinSourceVersionFor, func(context.Context, io.Writer, *ssh.Pool, *inventory.Manifest) ([]runningInstance, error) {
		return []runningInstance{{Service: "purser", Host: "billing", Version: "v0.3.14-rc4"}}, nil
	})
	defer restore()
	if err := enforceSourceFloor(t.Context(), &bytes.Buffer{}, &resolvedCluster{Manifest: &inventory.Manifest{}}, nil, "v0.3.14-rc3"); err == nil {
		t.Fatal("absence of generic floors must not bypass the invoice compatibility boundary")
	}
}

func containsPurser(names []string) bool {
	for _, n := range names {
		if n == "purser" {
			return true
		}
	}
	return false
}
