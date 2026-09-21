package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestYugabyteDatabaseMetadataCarriesLogicalLayoutSource(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu":    {Enabled: true, Deploy: "foghorn", Cluster: "media-eu-1"},
			"foghorn-us":    {Enabled: true, Deploy: "foghorn", Cluster: "media-us-1"},
			"quartermaster": {Enabled: true, Cluster: "control-1"},
		},
	}
	expanded := expandedYugabyteDatabaseConfigs([]inventory.DatabaseConfig{
		{Name: "foghorn", Owner: "foghorn"},
		{Name: "quartermaster", Owner: "quartermaster"},
	}, manifest)
	items := yugabyteDatabaseConfigsToMetadata(expanded, manifest, map[string]string{
		"DATABASE_PASSWORD":         "owner-secret",
		"DATABASE_RUNTIME_PASSWORD": "runtime-secret",
	}, nil, "owner-secret")

	got := map[string]string{}
	for _, item := range items {
		got[item["name"]] = item["layout_source"]
	}
	want := map[string]string{"foghorn_eu": "foghorn", "foghorn_us": "foghorn", "quartermaster": "quartermaster"}
	if len(got) != len(want) {
		t.Fatalf("metadata databases = %v, want %v", got, want)
	}
	for name, source := range want {
		if got[name] != source {
			t.Errorf("%s layout_source = %q, want %q", name, got[name], source)
		}
	}
}

func TestYugabyteLogicalDatabaseNameIgnoresDisabledAndUnclusteredServices(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {Enabled: false, Deploy: "foghorn", Cluster: "media-eu-1"},
			"foghorn-us": {Enabled: true, Deploy: "foghorn"},
		},
	}
	for _, name := range []string{"foghorn_eu", "foghorn_us", "purser"} {
		if got := yugabyteLogicalDatabaseName(name, manifest); got != name {
			t.Errorf("yugabyteLogicalDatabaseName(%q) = %q, want the name unchanged", name, got)
		}
	}
	if got := yugabyteLogicalDatabaseName("foghorn_eu", nil); got != "foghorn_eu" {
		t.Errorf("nil manifest mapped foghorn_eu to %q", got)
	}
}
