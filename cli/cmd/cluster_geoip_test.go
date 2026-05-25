package cmd

import (
	"slices"
	"strings"
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

func TestEffectiveGeoIPServices_DefaultIncludesAliasedLivepeerGateway(t *testing.T) {
	m := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway-eu": {Enabled: true, Deploy: "livepeer-gateway"},
		},
	}

	got := effectiveGeoIPServices(m, nil)
	want := []string{"livepeer-gateway"}
	if !slices.Equal(got, want) {
		t.Fatalf("default with aliased gateway = %v, want %v", got, want)
	}
}

func TestGeoIPTargetHostsExpandsDeploySlugAliases(t *testing.T) {
	m := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {},
			"regional-us-1": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway-eu": {Enabled: true, Deploy: "livepeer-gateway", Hosts: []string{"regional-eu-1"}},
			"livepeer-gateway-us": {Enabled: true, Deploy: "livepeer-gateway", Hosts: []string{"regional-us-1"}},
		},
	}

	got, err := geoIPTargetHosts(m, []string{"livepeer-gateway"})
	if err != nil {
		t.Fatalf("geoIPTargetHosts returned error: %v", err)
	}
	want := []string{"regional-eu-1", "regional-us-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("deploy target hosts = %v, want %v", got, want)
	}
}

func TestGeoIPTargetHostsAcceptsAliasedServiceName(t *testing.T) {
	m := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway-eu": {Enabled: true, Deploy: "livepeer-gateway", Hosts: []string{"regional-eu-1"}},
		},
	}

	got, err := geoIPTargetHosts(m, []string{"livepeer-gateway-eu"})
	if err != nil {
		t.Fatalf("geoIPTargetHosts returned error: %v", err)
	}
	want := []string{"regional-eu-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("aliased target hosts = %v, want %v", got, want)
	}
}

func TestGeoIPRemoteTempPathStaysBesideTarget(t *testing.T) {
	got := geoIPRemoteTempPath("/usr/share/GeoIP/GeoLite2-City.mmdb", "regional/eu 1")
	if !strings.HasPrefix(got, "/usr/share/GeoIP/.GeoLite2-City.mmdb.regional-eu-1.") {
		t.Fatalf("temp path %q is not beside target with sanitized host", got)
	}
	if !strings.HasSuffix(got, ".tmp") {
		t.Fatalf("temp path %q missing .tmp suffix", got)
	}
}

func TestAtomicGeoIPPublishCommandMovesTempIntoPlace(t *testing.T) {
	got := atomicGeoIPPublishCommand(
		"/usr/share/GeoIP/.GeoLite2-City.mmdb.tmp",
		"/usr/share/GeoIP/GeoLite2-City.mmdb",
		0644,
	)
	for _, want := range []string{
		"chmod 644 '/usr/share/GeoIP/.GeoLite2-City.mmdb.tmp'",
		"mv -f '/usr/share/GeoIP/.GeoLite2-City.mmdb.tmp' '/usr/share/GeoIP/GeoLite2-City.mmdb'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("publish command %q missing %q", got, want)
		}
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
