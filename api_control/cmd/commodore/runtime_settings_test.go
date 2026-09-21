package main

import (
	"testing"

	"frameworks/api_control/internal/appconfig"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func TestRuntimeSettingsCarriesEmailBranding(t *testing.T) {
	branding := config.EmailBranding{LogoURL: "https://cdn.example.test/logo.png", WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}
	settings := runtimeSettings(&appconfig.Commodore{EmailBranding: branding, DeviceVerificationURL: "https://login.example.test/device"})
	if settings.Branding != branding {
		t.Errorf("Branding = %+v, want %+v", settings.Branding, branding)
	}
	if settings.DeviceVerificationURL != "https://login.example.test/device" {
		t.Errorf("DeviceVerificationURL = %q", settings.DeviceVerificationURL)
	}
}
