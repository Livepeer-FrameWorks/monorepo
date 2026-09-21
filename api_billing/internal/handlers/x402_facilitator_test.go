package handlers

import (
	"testing"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/appconfig/appconfigtest"
)

func TestEmbeddedFacilitatorIsProductionDefault(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	appconfigtest.Set(t, "X402_FACILITATOR_PROVIDER", "")
	provider, client, err := newX402FacilitatorFromConfig(appconfig.Runtime())
	if err != nil {
		t.Fatalf("production embedded facilitator rejected: %v", err)
	}
	if provider != "self" || client != nil {
		t.Fatalf("provider=%q client=%T, want embedded self facilitator", provider, client)
	}
}

func TestEmbeddedFacilitatorCanBeSelectedExplicitlyInProduction(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	appconfigtest.Set(t, "X402_FACILITATOR_PROVIDER", "self")
	provider, client, err := newX402FacilitatorFromConfig(appconfig.Runtime())
	if err != nil {
		t.Fatalf("explicit production embedded facilitator rejected: %v", err)
	}
	if provider != "self" || client != nil {
		t.Fatalf("provider=%q client=%T, want embedded self facilitator", provider, client)
	}
}
