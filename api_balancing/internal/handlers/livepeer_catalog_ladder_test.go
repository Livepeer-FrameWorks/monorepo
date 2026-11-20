package handlers

import (
	"encoding/json"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/transcode/contract"
)

// catalogProcessesJSON composes the named ladder into the catalog's default
// tier policy, matching Purser's bootstrap output consumed by Foghorn.
func catalogProcessesJSON(t *testing.T, key string, choice ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api_billing", "internal", "bootstrap", "catalog", "billing_tiers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Ladders []struct {
			Name       string           `yaml:"name"`
			Renditions []map[string]any `yaml:"renditions"`
		} `yaml:"transcode_ladders"`
		Tiers []struct {
			Name    string `yaml:"tier_name"`
			Live    string `yaml:"processes_live"`
			VOD     string `yaml:"processes_vod"`
			Default string `yaml:"default_transcode_ladder"`
		} `yaml:"tiers"`
	}
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, tier := range catalog.Tiers {
		if tier.Name != "payg" {
			continue
		}
		name := tier.Default
		if len(choice) > 0 {
			name = choice[0]
		}
		for _, ladder := range catalog.Ladders {
			if ladder.Name != name {
				continue
			}
			raw, err := json.Marshal(ladder.Renditions)
			if err != nil {
				t.Fatal(err)
			}
			var renditions []contract.RenditionSpec
			if parseErr := json.Unmarshal(raw, &renditions); parseErr != nil {
				t.Fatal(parseErr)
			}
			policy := tier.Live
			if key == "processes_vod" {
				policy = tier.VOD
			}
			composed, err := mist.SetTranscodeLadder(policy, renditions)
			if err != nil {
				t.Fatal(err)
			}
			return composed
		}
	}
	t.Fatalf("catalog policy %s not found", key)
	return ""
}

// The catalog ABR ladder must yield exactly the renditions at or below the
// source for common sources: Foghorn's Livepeer auth spec (normalized
// profiles) and the Helmsman completeness ladder (short sides) are both
// derived from the same config and the same short-side inhibitor rule as Mist.
func TestCatalogABRLadderPerSource(t *testing.T) {
	sources := []struct {
		name   string
		source mist.SourceMediaInfo
		want   []int
	}{
		{"1080p30", mist.SourceMediaInfo{Width: 1920, Height: 1080, FPS: 30}, []int{360, 480, 720, 1080}},
		{"720p30", mist.SourceMediaInfo{Width: 1280, Height: 720, FPS: 30}, []int{360, 480, 720}},
		{"480p30", mist.SourceMediaInfo{Width: 854, Height: 480, FPS: 30}, []int{360, 480}},
		{"portrait 720x1280", mist.SourceMediaInfo{Width: 720, Height: 1280, FPS: 30}, []int{360, 480, 720}},
		{"1440p", mist.SourceMediaInfo{Width: 2560, Height: 1440}, []int{360, 480, 720, 1080}},
		{"2160p", mist.SourceMediaInfo{Width: 3840, Height: 2160}, []int{360, 480, 720, 1080}},
		{"1920x1200", mist.SourceMediaInfo{Width: 1920, Height: 1200}, []int{360, 480, 720, 1080}},
		{"960x540", mist.SourceMediaInfo{Width: 960, Height: 540}, []int{360, 480}},
		{"square", mist.SourceMediaInfo{Width: 1080, Height: 1080}, []int{360, 480, 720, 1080}},
		{"4:3", mist.SourceMediaInfo{Width: 1440, Height: 1080}, []int{360, 480, 720, 1080}},
		{"ultrawide", mist.SourceMediaInfo{Width: 3840, Height: 1080}, []int{360, 480, 720, 1080}},
		{"portrait 1080x1920", mist.SourceMediaInfo{Width: 1080, Height: 1920}, []int{360, 480, 720, 1080}},
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
				w, _ := p["width"].(int)
				got = append(got, min(w, h))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s/%s: auth profile heights = %v, want %v", key, tc.name, got, tc.want)
			}
			for _, policy := range []string{processesJSON, mist.ReplaceLivepeerWithLocal(processesJSON)} {
				expected, err := mist.ExpectedVideoRenditions(policy, tc.source)
				if err != nil {
					t.Fatalf("%s/%s: %v", key, tc.name, err)
				}
				if len(expected) != len(tc.want) {
					t.Fatalf("%s/%s: %d renditions, want %d", key, tc.name, len(expected), len(tc.want))
				}
				for i, r := range expected {
					if !mist.RenditionShortSideMatches(r.Width, r.Height, tc.want[i]) {
						t.Errorf("%s/%s: %dx%d does not match rung %d", key, tc.name, r.Width, r.Height, tc.want[i])
					}
				}
			}
		}
	}
}

func TestCatalogLargeLadders(t *testing.T) {
	for _, ladder := range []string{"qhd", "uhd"} {
		for _, source := range []mist.SourceMediaInfo{{Width: 2560, Height: 1440}, {Width: 3840, Height: 2160}, {Width: 1440, Height: 2560}, {Width: 2160, Height: 3840}} {
			for _, lifecycle := range []string{"processes_live", "processes_vod"} {
				policy := catalogProcessesJSON(t, lifecycle, ladder)
				for _, path := range []string{policy, mist.ReplaceLivepeerWithLocal(policy)} {
					expected, err := mist.ExpectedVideoRenditions(path, source)
					if err != nil {
						t.Fatal(err)
					}
					want := 5
					if ladder == "uhd" && source.Width*source.Height >= 3840*2160 {
						want = 6
					}
					if len(expected) != want {
						t.Fatalf("%s/%dx%d: %v, want %d rungs", ladder, source.Width, source.Height, expected, want)
					}
				}
			}
		}
	}
}
