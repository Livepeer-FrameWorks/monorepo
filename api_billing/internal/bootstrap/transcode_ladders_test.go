package bootstrap

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

const standardLadderProfiles = `[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<0x360"},{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<0x480"},{"name":"720p","bitrate":4500000,"fps":0,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<0x720"},{"name":"1080p","bitrate":7800000,"fps":0,"height":1080,"profile":"H264ConstrainedHigh","track_inhibit":"video=<0x1080"}]`

func TestEmbeddedCatalogTranscodeLadders(t *testing.T) {
	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range tiers {
		want := 3
		if tier.TierName == "free" {
			want = 1
		}
		if len(tier.TranscodeLadders) != want || tier.DefaultTranscodeLadder != "standard" {
			t.Fatalf("%s: ladders=%v default=%q", tier.TierName, tier.TranscodeLadders, tier.DefaultTranscodeLadder)
		}
		for _, policy := range []string{tier.ProcessesLive, tier.ProcessesVOD} {
			profiles, has, err := transcodeEntryRenditions(policy)
			got, marshalErr := json.Marshal(profiles)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err != nil || !has || !jsonEq(string(got), []byte(standardLadderProfiles)) {
				t.Fatalf("%s has incorrect standard ladder profiles: %s (%v)", tier.TierName, got, err)
			}
			if !mist.HasTranscodeProcesses(policy) {
				t.Fatalf("%s lost video transcode policy", tier.TierName)
			}
		}
		for _, ladder := range tier.TranscodeLadders {
			want := 4
			if ladder.Name == "qhd" {
				want = 5
			}
			if ladder.Name == "uhd" {
				want = 6
			}
			if len(ladder.Renditions) != want {
				t.Fatalf("%s/%s: %d rungs, want %d", tier.TierName, ladder.Name, len(ladder.Renditions), want)
			}
		}
	}
}

func TestCatalogRefusesIncoherentLadderDefinitions(t *testing.T) {
	for name, change := range map[string]func(*catalog){
		"undefined":            func(c *catalog) { c.Tiers[0].AllowedTranscodeLadderNames = []string{"missing"} },
		"disallowed default":   func(c *catalog) { c.Tiers[0].DefaultTranscodeLadder = "missing" },
		"duplicate definition": func(c *catalog) { c.TranscodeLadders = append(c.TranscodeLadders, c.TranscodeLadders[0]) },
		"inline profiles": func(c *catalog) {
			c.Tiers[0].ProcessesLive = `[{"process":"Livepeer","target_profiles":[{"name":"other","height":720}]}]`
		},
	} {
		t.Run(name, func(t *testing.T) {
			var c catalog
			if err := yaml.Unmarshal(embeddedBillingTiersYAML, &c); err != nil {
				t.Fatal(err)
			}
			change(&c)
			err := composeTranscodeLadders(c.TranscodeLadders, c.Tiers)
			if err == nil {
				err = validateCatalogTranscodeLadders(c.Tiers)
			}
			if err == nil {
				t.Fatal("incoherent catalog accepted")
			}
		})
	}
}
