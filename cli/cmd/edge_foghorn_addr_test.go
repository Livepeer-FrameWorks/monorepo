package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

// A pinned list reaches FOGHORN_CONTROL_ADDR with every instance; without a
// pin the edge gets the cell's Foghorn name, which resolves to every instance.
func TestEdgeManifestNodeFoghornAddrRendersEveryInstance(t *testing.T) {
	t.Parallel()
	pinned := inventory.EdgeNode{
		Name:        "fw-stg-edge-eu",
		FoghornAddr: " 192.168.10.17:18029, 192.168.10.18:18029,192.168.10.19:18029 ",
	}
	if got, want := edgeManifestNodeFoghornAddr(pinned, "staging.frameworks.network", "staging-media-eu"), "192.168.10.17:18029,192.168.10.18:18029,192.168.10.19:18029"; got != want {
		t.Fatalf("pinned list rendered %q, want %q", got, want)
	}
	unpinned := inventory.EdgeNode{Name: "edge-eu-1"}
	if got, want := edgeManifestNodeFoghornAddr(unpinned, "frameworks.network", "media-eu-1"), "foghorn.media-eu-1.frameworks.network:18029"; got != want {
		t.Fatalf("unpinned node rendered %q, want the cell name %q", got, want)
	}
}

func TestEdgeFoghornUsesInternalCAChecksEveryEntry(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"foghorn.media-eu-1.frameworks.network:18029":                        false,
		"192.168.10.17:18029,192.168.10.18:18029":                            true,
		"foghorn.media-eu-1.frameworks.network:18029,foghorn.internal:18019": true,
	}
	for addr, want := range cases {
		if got := edgeFoghornUsesInternalCA(addr); got != want {
			t.Errorf("edgeFoghornUsesInternalCA(%q) = %v, want %v", addr, got, want)
		}
	}
}
