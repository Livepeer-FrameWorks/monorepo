package handlers

import "testing"

func TestEmbeddedFacilitatorIsProductionDefault(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")
	t.Setenv("X402_FACILITATOR_PROVIDER", "")
	provider, client, err := newX402FacilitatorFromEnv()
	if err != nil {
		t.Fatalf("production embedded facilitator rejected: %v", err)
	}
	if provider != "self" || client != nil {
		t.Fatalf("provider=%q client=%T, want embedded self facilitator", provider, client)
	}
}

func TestEmbeddedFacilitatorCanBeSelectedExplicitlyInProduction(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")
	t.Setenv("X402_FACILITATOR_PROVIDER", "self")
	provider, client, err := newX402FacilitatorFromEnv()
	if err != nil {
		t.Fatalf("explicit production embedded facilitator rejected: %v", err)
	}
	if provider != "self" || client != nil {
		t.Fatalf("provider=%q client=%T, want embedded self facilitator", provider, client)
	}
}
