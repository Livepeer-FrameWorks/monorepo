package control

import "testing"

func TestFoghornBalancerBaseUsesClusterScopedDNS(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "frameworks.network")
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.frameworks.network")

	got := foghornBalancerBase("core-central-primary")
	want := "https://foghorn.core-central-primary.frameworks.network"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}

func TestFoghornBalancerBaseFallsBackToEnv(t *testing.T) {
	t.Setenv("FOGHORN_PUBLIC_BASE", "https://foghorn.example")

	got := foghornBalancerBase("")
	want := "https://foghorn.example"
	if got != want {
		t.Fatalf("foghornBalancerBase() = %q, want %q", got, want)
	}
}
