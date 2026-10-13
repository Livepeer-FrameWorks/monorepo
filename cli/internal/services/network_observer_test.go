package services

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/provisioner"
)

func TestNetworkObserverCanBeSelectedWithoutEnrollingExternalProbers(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []struct{ profile, include string }{
		{include: "network-observer"},
		{profile: "network-observations"},
	} {
		specs, err := ResolveSelection(catalog, selection.profile, selection.include, "")
		if err != nil {
			t.Errorf("select observer %+v: %v", selection, err)
			continue
		}
		if len(specs) != 1 || specs[0].Name != "network-observer" {
			t.Fatalf("observer selection = %+v", specs)
		}
		if specs[0].Health.Port != 18024 || specs[0].Health.Path != "/health" {
			t.Errorf("observer health = %+v", specs[0].Health)
		}
	}
	for profile, names := range catalog.Profiles {
		if slices.Contains(names, "network-prober") {
			t.Errorf("profile %s enrolls external probers", profile)
		}
	}
	for _, profile := range []string{"central-all", "platform"} {
		if slices.Contains(catalog.Profiles[profile], "network-observer") {
			t.Errorf("%s starts the observer; a platform enables it by declaring it in the cluster manifest", profile)
		}
	}
	if got := provisioner.ServicePorts["network-observer"]; got != 18024 {
		t.Errorf("observer provisioner port = %d, want 18024", got)
	}
	if _, err := provisioner.GetProvisioner("network-observer", nil); err != nil {
		t.Errorf("observer provisioner: %v", err)
	}
}
