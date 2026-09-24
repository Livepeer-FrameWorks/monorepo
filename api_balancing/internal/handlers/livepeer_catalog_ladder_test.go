package handlers

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// catalogProcessesJSON returns the raw process JSON anchored under key in the
// billing catalog. The anchored block holds the one-line JSON array that every
// tier aliases, so reading it avoids a YAML dependency in this module.
func catalogProcessesJSON(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "api_billing", "internal", "bootstrap", "catalog", "billing_tiers.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+": &") && strings.HasSuffix(trimmed, "|") && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	t.Fatalf("catalog key %q with anchored block not found", key)
	return ""
}

// The catalog ABR ladder must yield exactly the renditions at or below the
// source for common sources: Foghorn's Livepeer auth spec (normalized
// profiles) and the Helmsman completeness ladder (requested heights) are both
// derived from the same config and the same area inhibitor rule as Mist.
func TestCatalogABRLadderPerSource(t *testing.T) {
	sources := []struct {
		name   string
		source mist.SourceMediaInfo
		want   []int
	}{
		{"1080p30", mist.SourceMediaInfo{Width: 1920, Height: 1080, FPS: 30}, []int{360, 480, 720, 1080}},
		{"720p30", mist.SourceMediaInfo{Width: 1280, Height: 720, FPS: 30}, []int{360, 480, 720}},
		{"480p30", mist.SourceMediaInfo{Width: 854, Height: 480, FPS: 30}, []int{360, 480}},
		// Portrait 720x1280 has the area of 720p: the 1080p rung is an upscale.
		// The auth spec maps each rung onto the portrait axis, so only the
		// requested ladder heights are compared for it.
		{"portrait 720x1280", mist.SourceMediaInfo{Width: 720, Height: 1280, FPS: 30}, []int{360, 480, 720}},
	}
	// Foghorn stamps the workload contract before the config reaches Mist or
	// the auth spec, so the test stamps it the same way.
	stamped := map[string]string{
		"processes_live": mist.SetLivepeerWorkload(catalogProcessesJSON(t, "processes_live"), mist.WorkloadLive, 0, 0),
		"processes_vod":  mist.SetLivepeerWorkload(catalogProcessesJSON(t, "processes_vod"), mist.WorkloadVOD, 30000, 0.5),
	}
	for _, key := range []string{"processes_live", "processes_vod"} {
		processesJSON := stamped[key]
		spec, err := mist.LivepeerJobSpecFromProcessesJSON(processesJSON)
		if err != nil {
			t.Fatalf("%s: parse Livepeer spec: %v", key, err)
		}
		for _, tc := range sources {
			profiles := mist.NormalizeLivepeerProfiles(spec.Profiles, tc.source)
			if len(profiles) != len(tc.want) {
				t.Errorf("%s/%s: %d auth profiles, want %d", key, tc.name, len(profiles), len(tc.want))
			}
			var got []int
			for _, p := range profiles {
				h, ok := p["height"].(int)
				if !ok {
					t.Fatalf("%s/%s: normalized profile %v has no int height", key, tc.name, p)
				}
				got = append(got, h)
			}
			if tc.source.Width >= tc.source.Height && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s/%s: auth profile heights = %v, want %v", key, tc.name, got, tc.want)
			}
			heights, err := mist.RequestedRenditionHeights(processesJSON, tc.source)
			if err != nil {
				t.Fatalf("%s/%s: requested heights: %v", key, tc.name, err)
			}
			if !reflect.DeepEqual(heights, tc.want) {
				t.Errorf("%s/%s: requested heights = %v, want %v", key, tc.name, heights, tc.want)
			}
		}
	}
}
