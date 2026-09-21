package config

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
)

// blockConfig embeds two validating blocks and declares its own Validate, so
// neither block's Validate is promoted; Load must still run both.
type blockConfig struct {
	Logging
	SystemTenant
	GRPCMetadataPolicy
}

func (c *blockConfig) Validate() error { return nil }

func TestLoadValidatesNestedBlocks(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"log level":       {map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL must be"},
		"system tenant":   {map[string]string{"SYSTEM_TENANT_ID": "not-a-uuid"}, "SYSTEM_TENANT_ID must be a UUID"},
		"metadata policy": {map[string]string{"GRPC_METADATA_POLICY": "permissive"}, "GRPC_METADATA_POLICY must be"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load[blockConfig](Options{Lookup: lookupFrom(tc.env)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBlockDefaults(t *testing.T) {
	cfg, err := Load[blockConfig](Options{Lookup: lookupFrom(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "info" || cfg.MetadataPolicy != MetadataPolicyAllow {
		t.Fatalf("defaults = log %q policy %q", cfg.LogLevel, cfg.MetadataPolicy)
	}
	if got := cfg.SystemTenantUUID(); got != tenants.SystemTenantID {
		t.Fatalf("SystemTenantUUID() = %s, want reserved %s", got, tenants.SystemTenantID)
	}
}

func TestSystemTenantUUIDUsesConfiguredID(t *testing.T) {
	want := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	cfg, err := Load[blockConfig](Options{Lookup: lookupFrom(map[string]string{"SYSTEM_TENANT_ID": want.String()})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.SystemTenantUUID(); got != want {
		t.Fatalf("SystemTenantUUID() = %s, want %s", got, want)
	}
}

func TestLoggingApplySetsLevel(t *testing.T) {
	cfg, err := Load[blockConfig](Options{Lookup: lookupFrom(map[string]string{"LOG_LEVEL": "warn"})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	cfg.ApplyLogLevel(logger)
	if logger.GetLevel() != logrus.WarnLevel {
		t.Fatalf("level = %s, want warn", logger.GetLevel())
	}
}

func TestRuntimeEnumAliasesAndEdgeLoggingFallback(t *testing.T) {
	cfg, err := Load[blockConfig](Options{Lookup: lookupFrom(map[string]string{"LOG_LEVEL": "Warning", "GRPC_METADATA_POLICY": "Deny"})})
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	cfg.ApplyLogLevel(logger)
	if logger.GetLevel() != logrus.WarnLevel {
		t.Fatal("warning alias was not applied")
	}
	edge, err := Load[TolerantLogging](Options{Lookup: lookupFrom(map[string]string{"LOG_LEVEL": "custom"})})
	if err != nil {
		t.Fatal(err)
	}
	edge.ApplyLogLevel(logger)
	if logger.GetLevel() != logrus.WarnLevel {
		t.Fatal("unknown edge level changed the logger")
	}
}

func TestEmailBrandingLogo(t *testing.T) {
	cases := map[string]struct {
		branding EmailBranding
		want     string
	}{
		"configured logo wins":   {EmailBranding{LogoURL: " https://cdn.example.test/logo.png ", WebAppURL: "https://app.example.test"}, "https://cdn.example.test/logo.png"},
		"default under web app":  {EmailBranding{WebAppURL: "https://app.example.test/app/"}, "https://app.example.test/app/frameworks-light-logomark.png"},
		"nothing configured":     {EmailBranding{}, ""},
		"blank web app is unset": {EmailBranding{WebAppURL: "  "}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.branding.Logo(); got != tc.want {
				t.Fatalf("Logo() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEmailBrandingSupport(t *testing.T) {
	if got := (EmailBranding{}).Support(); got != DefaultSupportEmail {
		t.Fatalf("Support() = %q, want %q", got, DefaultSupportEmail)
	}
	if got := (EmailBranding{SupportEmail: "  "}).Support(); got != DefaultSupportEmail {
		t.Fatalf("blank Support() = %q, want %q", got, DefaultSupportEmail)
	}
	if got := (EmailBranding{SupportEmail: " help@example.test "}).Support(); got != "help@example.test" {
		t.Fatalf("Support() = %q, want help@example.test", got)
	}
}

func TestEmailBrandingLoadsFromEnvironment(t *testing.T) {
	type brandedConfig struct {
		EmailBranding
	}
	cfg, err := Load[brandedConfig](Options{Lookup: lookupFrom(map[string]string{
		"EMAIL_LOGO_URL":    "https://cdn.example.test/logo.png",
		"WEBAPP_PUBLIC_URL": "https://app.example.test",
		"SUPPORT_EMAIL":     "help@example.test",
	})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := EmailBranding{LogoURL: "https://cdn.example.test/logo.png", WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}
	if cfg.EmailBranding != want {
		t.Fatalf("EmailBranding = %+v, want %+v", cfg.EmailBranding, want)
	}
}
