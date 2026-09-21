package appconfig_test

import (
	"testing"

	"frameworks/api_control/internal/appconfig"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestCommodoreEmailBranding(t *testing.T) {
	cfg, err := config.Load[appconfig.Commodore](config.Options{Service: "commodore", Lookup: lookupFrom(map[string]string{
		"DATABASE_URL":         "postgres://commodore",
		"JWT_SECRET":           "jwt",
		"SERVICE_TOKEN":        "token",
		"FIELD_ENCRYPTION_KEY": "field-key",
		"USAGE_HASH_SECRET":    "usage-hash",
		"BUILD_ENV":            "development",
		"EMAIL_LOGO_URL":       "https://cdn.example.test/logo.png",
		"WEBAPP_PUBLIC_URL":    "https://app.example.test",
		"SUPPORT_EMAIL":        "help@example.test",
	})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.EmailBranding{LogoURL: "https://cdn.example.test/logo.png", WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}
	if cfg.EmailBranding != want {
		t.Errorf("EmailBranding = %+v, want %+v", cfg.EmailBranding, want)
	}
}

func TestCommodoreBootstrapDeclaresNoEmailBranding(t *testing.T) {
	if _, err := config.Load[appconfig.CommodoreBootstrap](config.Options{Service: "commodore", Lookup: lookupFrom(map[string]string{
		"DATABASE_URL":  "postgres://commodore",
		"SERVICE_TOKEN": "token",
	})}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	fields, err := config.Describe(&appconfig.CommodoreBootstrap{}, config.Options{Lookup: lookupFrom(nil)})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		switch field.Key {
		case "EMAIL_LOGO_URL", "WEBAPP_PUBLIC_URL", "SUPPORT_EMAIL":
			t.Errorf("bootstrap variant declares %s", field.Key)
		}
	}
}
