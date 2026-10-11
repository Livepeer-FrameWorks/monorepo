package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestNetworkProberReleaseHashIncludesInstallationAssets(t *testing.T) {
	repo := filepath.Clean(filepath.Join("..", ".."))
	components, err := LoadComponentsFromFile(filepath.Join(repo, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range components.Services {
		if component.Name != "network-prober" {
			continue
		}
		files, err := collectExtraHashFiles(repo, component.ExtraHashPaths)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"install-network-prober.sh", "network-prober.service", "network-prober.env.example", "package-network-prober.sh"} {
			want := filepath.Join(repo, "api_network_observer", "deploy", name)
			if !slices.Contains(files, want) {
				t.Errorf("release source hash excludes installation asset %s", name)
			}
		}
		return
	}
	t.Fatal("prober is missing from release components")
}
