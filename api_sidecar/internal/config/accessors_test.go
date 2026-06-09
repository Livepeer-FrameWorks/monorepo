package config

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// GetTenantID / GetOperationalMode read the last applied ConfigSeed via the
// package-global manager. They must be nil-safe (no manager, no seed) and apply
// the NORMAL fallback when the seed leaves the mode UNSPECIFIED.
func TestSeedAccessors(t *testing.T) {
	prev := manager
	t.Cleanup(func() { manager = prev })

	t.Run("nil manager returns defaults", func(t *testing.T) {
		manager = nil
		if got := GetTenantID(); got != "" {
			t.Fatalf("GetTenantID with nil manager = %q, want empty", got)
		}
		if got := GetOperationalMode(); got != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL {
			t.Fatalf("GetOperationalMode with nil manager = %v, want NORMAL", got)
		}
	})

	t.Run("manager without a seed returns defaults", func(t *testing.T) {
		manager = &Manager{}
		if got := GetTenantID(); got != "" {
			t.Fatalf("GetTenantID with no seed = %q, want empty", got)
		}
		if got := GetOperationalMode(); got != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL {
			t.Fatalf("GetOperationalMode with no seed = %v, want NORMAL", got)
		}
	})

	t.Run("unspecified mode falls back to NORMAL", func(t *testing.T) {
		manager = &Manager{lastSeed: &ipcpb.ConfigSeed{
			TenantId:        "tenant-7",
			OperationalMode: ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED,
		}}
		if got := GetTenantID(); got != "tenant-7" {
			t.Fatalf("GetTenantID = %q, want tenant-7", got)
		}
		if got := GetOperationalMode(); got != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL {
			t.Fatalf("GetOperationalMode (unspecified) = %v, want NORMAL fallback", got)
		}
	})

	t.Run("explicit mode is reported verbatim", func(t *testing.T) {
		manager = &Manager{lastSeed: &ipcpb.ConfigSeed{
			OperationalMode: ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING,
		}}
		if got := GetOperationalMode(); got != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING {
			t.Fatalf("GetOperationalMode = %v, want DRAINING", got)
		}
	})
}

func TestParseFloat64(t *testing.T) {
	cases := map[string]float64{
		"":     0,
		"0.85": 0.85,
		"-1.5": -1.5,
		"abc":  0, // parse error → silent zero
		"12":   12,
	}
	for in, want := range cases {
		if got := parseFloat64(in); got != want {
			t.Errorf("parseFloat64(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGetStorageCapacityBytes(t *testing.T) {
	t.Setenv("HELMSMAN_STORAGE_CAPACITY_BYTES", "1048576")
	if got := GetStorageCapacityBytes(); got != 1048576 {
		t.Fatalf("GetStorageCapacityBytes = %d, want 1048576", got)
	}
	t.Setenv("HELMSMAN_STORAGE_CAPACITY_BYTES", "")
	if got := GetStorageCapacityBytes(); got != 0 {
		t.Fatalf("GetStorageCapacityBytes (unset) = %d, want 0", got)
	}
}

func TestGrpcCABundlePath(t *testing.T) {
	t.Setenv("GRPC_TLS_CA_PATH", "/custom/ca.crt")
	if got := grpcCABundlePath(); got != "/custom/ca.crt" {
		t.Fatalf("grpcCABundlePath = %q, want /custom/ca.crt", got)
	}
	t.Setenv("GRPC_TLS_CA_PATH", "  ") // whitespace-only → default
	if got := grpcCABundlePath(); got != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("grpcCABundlePath (blank) = %q, want default", got)
	}
}
