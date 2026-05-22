package control

import "testing"

func TestFoghornBalancerBaseUsesClusterScopedDNS(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "frameworks.network")

	got := foghornBalancerBase("core-central-primary")
	want := "https://foghorn.core-central-primary.frameworks.network"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseNormalizesBrandDomain(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "https://frameworks.network/")

	got := foghornBalancerBase("media-eu-1")
	want := "https://foghorn.media-eu-1.frameworks.network"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseUsesExplicitPublicBase(t *testing.T) {
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")

	got := foghornBalancerBase("core-central-primary")
	want := "https://foghorn.example"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseUsesLocalComposeURL(t *testing.T) {
	t.Setenv("BUILD_ENV", "development")
	t.Setenv("FOGHORN_URL", "http://foghorn:18008")

	got := foghornBalancerBase("central-primary")
	want := "http://foghorn:18008"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestComposeConfigSeedScopesRealtimeToProcessing(t *testing.T) {
	seed := composeConfigSeed("node-1", nil, "", 0, "")

	realtimeByName := map[string]bool{}
	for _, template := range seed.GetTemplates() {
		def := template.GetDef()
		realtimeByName[def.GetName()] = def.GetRealtime()
	}

	want := map[string]bool{
		"live":       false,
		"vod":        false,
		"dvr":        false,
		"processing": true,
		"pull":       false,
	}
	for name, realtime := range want {
		got, ok := realtimeByName[name]
		if !ok {
			t.Fatalf("template %q missing from ConfigSeed", name)
		}
		if got != realtime {
			t.Fatalf("template %q realtime = %v, want %v", name, got, realtime)
		}
	}
}
