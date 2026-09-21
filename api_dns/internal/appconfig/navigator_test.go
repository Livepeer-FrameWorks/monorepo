package appconfig

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func navigatorEnv() map[string]string {
	return map[string]string{
		"NAVIGATOR_PORT":        "18010",
		"NAVIGATOR_GRPC_PORT":   "18011",
		"SERVICE_TOKEN":         "token",
		"FIELD_ENCRYPTION_KEY":  "field-key",
		"DATABASE_URL":          "postgres://navigator",
		"BRAND_DOMAIN":          "frameworks.network",
		"ACME_EMAIL":            "ops@frameworks.network",
		"CLOUDFLARE_API_TOKEN":  "cf-token",
		"CLOUDFLARE_ZONE_ID":    "zone",
		"CLOUDFLARE_ACCOUNT_ID": "account",
	}
}

func loadNavigator(env map[string]string) (*Navigator, error) {
	return config.Load[Navigator](config.Options{
		Service: "navigator",
		Lookup: func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		},
	})
}

func TestNavigatorDefaults(t *testing.T) {
	cfg, err := loadNavigator(navigatorEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenHTTPPort() != "18010" || cfg.GRPCPort != "18011" {
		t.Fatalf("ports = %q/%q", cfg.ListenHTTPPort(), cfg.GRPCPort)
	}
	if cfg.QuartermasterGRPCAddr != "quartermaster:19002" || cfg.AdvertiseHost != "navigator" {
		t.Fatalf("addresses = %q/%q", cfg.QuartermasterGRPCAddr, cfg.AdvertiseHost)
	}
	if cfg.DecklogGRPCAddr != "decklog:18006" {
		t.Fatalf("DECKLOG_GRPC_ADDR default = %q", cfg.DecklogGRPCAddr)
	}
	if cfg.DNSRecordTTLSeconds != 60 || cfg.DNSLoadBalancerTTLSeconds != 60 || cfg.DNSHealthStaleSeconds != 300 {
		t.Fatalf("dns defaults = %d/%d/%d", cfg.DNSRecordTTLSeconds, cfg.DNSLoadBalancerTTLSeconds, cfg.DNSHealthStaleSeconds)
	}
	if cfg.LoadBalancerMonitorInterval != 60 || cfg.LoadBalancerMonitorTimeout != 5 || cfg.LoadBalancerMonitorRetries != 2 {
		t.Fatalf("monitor defaults = %d/%d/%d", cfg.LoadBalancerMonitorInterval, cfg.LoadBalancerMonitorTimeout, cfg.LoadBalancerMonitorRetries)
	}
	if cfg.DNSReconcileIntervalSeconds != 60 || cfg.AliasApplyStateIntervalSeconds != 15 {
		t.Fatalf("worker defaults = %d/%d", cfg.DNSReconcileIntervalSeconds, cfg.AliasApplyStateIntervalSeconds)
	}
	if cfg.IsProduction() {
		t.Fatal("empty BUILD_ENV must not be production")
	}
	if !cfg.DNSRecordsEnabled {
		t.Fatal("public DNS publication must be enabled by default")
	}
}

func TestNavigatorStagingDisablesDNSRecords(t *testing.T) {
	env := navigatorEnv()
	env["NAVIGATOR_DNS_RECORDS_ENABLED"] = "false"
	cfg, err := loadNavigator(env)
	if err != nil || cfg.DNSRecordsEnabled {
		t.Fatalf("staging DNS configuration: cfg=%+v err=%v", cfg, err)
	}
}

func TestNavigatorRequiresOperatorInputs(t *testing.T) {
	env := navigatorEnv()
	for _, key := range []string{"ACME_EMAIL", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ZONE_ID", "CLOUDFLARE_ACCOUNT_ID", "NAVIGATOR_PORT", "NAVIGATOR_GRPC_PORT"} {
		delete(env, key)
	}
	_, err := loadNavigator(env)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	for _, key := range []string{"ACME_EMAIL", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ZONE_ID", "CLOUDFLARE_ACCOUNT_ID", "NAVIGATOR_PORT", "NAVIGATOR_GRPC_PORT"} {
		if !slices.Contains(loadErr.Missing, key) {
			t.Errorf("missing list %v does not name %s", loadErr.Missing, key)
		}
	}
}

func TestNavigatorPortOverride(t *testing.T) {
	env := navigatorEnv()
	env["PORT"] = "28010"
	cfg, err := loadNavigator(env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenHTTPPort() != "28010" {
		t.Fatalf("PORT must take precedence over NAVIGATOR_PORT, got %q", cfg.ListenHTTPPort())
	}
}

func TestNavigatorValidateRequiresPairedHTTPTLSFiles(t *testing.T) {
	for _, key := range []string{"NAVIGATOR_HTTP_TLS_CERT_FILE", "NAVIGATOR_HTTP_TLS_KEY_FILE"} {
		env := navigatorEnv()
		env[key] = "/etc/frameworks/navigator.pem"
		_, err := loadNavigator(env)
		if err == nil || !strings.Contains(err.Error(), "must be set together") {
			t.Fatalf("%s alone: expected pairing error, got %v", key, err)
		}
	}

	env := navigatorEnv()
	env["NAVIGATOR_HTTP_TLS_CERT_FILE"] = "/etc/frameworks/navigator.pem"
	env["NAVIGATOR_HTTP_TLS_KEY_FILE"] = "/etc/frameworks/navigator.key"
	if _, err := loadNavigator(env); err != nil {
		t.Fatalf("paired TLS files: %v", err)
	}
}

func TestNavigatorIsProduction(t *testing.T) {
	for value, want := range map[string]bool{"production": true, " PROD ": true, "prod": true, "staging": false, "dev": false} {
		cfg := &Navigator{BuildEnvironment: config.BuildEnvironment{BuildEnv: value}}
		if got := cfg.IsProduction(); got != want {
			t.Errorf("BUILD_ENV=%q: IsProduction = %v, want %v", value, got, want)
		}
	}
}
