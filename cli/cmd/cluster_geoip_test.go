package cmd

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestEffectiveGeoIPServices_ExplicitWins(t *testing.T) {
	got := effectiveGeoIPServices(nil, []string{"foghorn"})
	if !slices.Equal(got, []string{"foghorn"}) {
		t.Fatalf("explicit services = %v, want [foghorn]", got)
	}
}

func TestEffectiveGeoIPServices_ManifestOverride(t *testing.T) {
	m := &inventory.Manifest{
		GeoIP: &inventory.GeoIPConfig{Services: []string{"chandler"}},
	}
	got := effectiveGeoIPServices(m, nil)
	if !slices.Equal(got, []string{"chandler"}) {
		t.Fatalf("manifest override = %v, want [chandler]", got)
	}
}

func TestEffectiveGeoIPServices_DefaultIncludesLivepeerGateway(t *testing.T) {
	m := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn":          {Enabled: true},
			"quartermaster":    {Enabled: true},
			"livepeer-gateway": {Enabled: true},
		},
	}
	got := effectiveGeoIPServices(m, nil)
	want := []string{"foghorn", "quartermaster", "livepeer-gateway"}
	if !slices.Equal(got, want) {
		t.Fatalf("default with all three present = %v, want %v", got, want)
	}
}

func TestEffectiveGeoIPServices_DefaultFiltersAbsent(t *testing.T) {
	m := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn":       {Enabled: true},
			"quartermaster": {Enabled: true},
			// no livepeer-gateway in manifest
		},
	}
	got := effectiveGeoIPServices(m, nil)
	want := []string{"foghorn", "quartermaster"}
	if !slices.Equal(got, want) {
		t.Fatalf("default without gateway = %v, want %v", got, want)
	}
}

// TestGeoIPEnvAndUploadSetsAligned pins the invariant that any service
// receiving GEOIP_MMDB_PATH from cluster_provision.go is also a default
// upload target — otherwise provision configures a path whose MMDB it never
// uploaded. cluster_provision.go reads from effectiveGeoIPServices for both
// sides; this test pins that contract.
func TestGeoIPEnvAndUploadSetsAligned(t *testing.T) {
	envSet := []string{"foghorn", "quartermaster", "livepeer-gateway"}

	m := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{},
	}
	for _, name := range envSet {
		m.Services[name] = inventory.ServiceConfig{Enabled: true}
	}

	got := effectiveGeoIPServices(m, nil)
	for _, name := range envSet {
		if !slices.Contains(got, name) {
			t.Errorf("env-injection target %q missing from default upload set %v", name, got)
		}
	}
}

// TestGeoIPExplicitOverrideAlignsEnvInjection guarantees the env-injection
// site only injects GEOIP_MMDB_PATH for services in the upload set. If a
// manifest explicitly sets geoip.services to omit livepeer-gateway, the
// gateway must NOT receive the env var either, otherwise it points at a
// path whose MMDB never got uploaded.
func TestGeoIPExplicitOverrideAlignsEnvInjection(t *testing.T) {
	m := &inventory.Manifest{
		GeoIP: &inventory.GeoIPConfig{
			Enabled:  true,
			Services: []string{"foghorn"}, // explicit: omit gateway + quartermaster
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn":          {Enabled: true},
			"quartermaster":    {Enabled: true},
			"livepeer-gateway": {Enabled: true},
		},
	}

	uploadSet := effectiveGeoIPServices(m, nil)
	if !slices.Equal(uploadSet, []string{"foghorn"}) {
		t.Fatalf("explicit override should yield [foghorn]; got %v", uploadSet)
	}
	for _, omitted := range []string{"quartermaster", "livepeer-gateway"} {
		if slices.Contains(uploadSet, omitted) {
			t.Errorf("upload set unexpectedly contains %q under explicit override", omitted)
		}
	}
}
