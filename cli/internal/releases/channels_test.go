package releases

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogDeclaresReleaseChannels(t *testing.T) {
	channels, err := Channels()
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(channels) != 3 || channels[0].Name != "stable" || channels[1].Name != "candidate" || channels[2].Name != "rc" {
		t.Fatalf("channels = %+v, want stable, candidate, rc", channels)
	}
	for _, ch := range channels {
		if ch.Tags == "" || ch.Support == "" {
			t.Fatalf("channel %s has empty documentation text", ch.Name)
		}
	}
}

func TestCatalogRejectsChannelDrift(t *testing.T) {
	base := "service_databases: {}\nreleases: []\nchannels:\n"
	stable := "  - {name: stable, tags: plain tags, image_track: latest, support: production}\n"
	candidate := "  - {name: candidate, tags: all tags, image_track: '', support: staging}\n"
	rc := "  - {name: rc, tags: prerelease tags, image_track: rc, support: validation}\n"
	cases := map[string]string{
		"wrong order":       base + rc + candidate + stable,
		"wrong image track": base + strings.Replace(stable, "image_track: latest", "image_track: stable", 1) + candidate + rc,
		"missing channel":   base + stable,
		"empty support":     base + stable + candidate + strings.Replace(rc, "support: validation", `support: ""`, 1),
		"extra channel":     base + stable + candidate + rc + "  - {name: beta, tags: beta tags, image_track: beta, support: none}\n",
		"unknown field":     base + stable + candidate + strings.Replace(rc, "support: validation", "support: validation, lts: true", 1),
	}
	for name, doc := range cases {
		if state := parseCatalog([]byte(doc)); state.err == nil {
			t.Errorf("%s: expected the catalog to be rejected", name)
		}
	}
	if state := parseCatalog([]byte(base + stable + candidate + rc)); state.err != nil {
		t.Fatalf("valid channels rejected: %v", state.err)
	}
}
