package appconfig

import (
	"errors"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadLookout(values map[string]string) (*Lookout, error) {
	return config.Load[Lookout](config.Options{
		Service: "lookout",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
}

func requiredLookout() map[string]string {
	return map[string]string{
		"DATABASE_URL":               "postgres://lookout@db:5432/lookout",
		"SERVICE_TOKEN":              "token",
		"QUARTERMASTER_GRPC_ADDR":    "quartermaster:19002",
		"DECKLOG_GRPC_ADDR":          "decklog:18006",
		"KAFKA_BROKERS":              "kafka-1:9092, kafka-2:9092",
		"LOOKOUT_ALERTMANAGER_TOKEN": "alertmanager-token",
	}
}

func TestLookoutDefaults(t *testing.T) {
	cfg, err := loadLookout(requiredLookout())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "18022" || cfg.GRPCPort != "19008" {
		t.Fatalf("ports = http %q grpc %q", cfg.HTTPListenPort(), cfg.GRPCPort)
	}
	if cfg.AdvertiseHost != "lookout" || cfg.KafkaClusterID != "local" {
		t.Fatalf("advertise host %q, kafka cluster %q", cfg.AdvertiseHost, cfg.KafkaClusterID)
	}
	if !slices.Equal(cfg.KafkaBrokers, []string{"kafka-1:9092", "kafka-2:9092"}) {
		t.Fatalf("KafkaBrokers = %v", cfg.KafkaBrokers)
	}
	if cfg.SMTPHost != "" || cfg.SMTPPort != "587" || cfg.FromEmail != "noreply@frameworks.network" || cfg.FromName != "FrameWorks" || cfg.SMTPAllowInsecure {
		t.Fatalf("unexpected SMTP defaults: %+v", cfg)
	}
	if len(cfg.NotifyEmailTo) != 0 || cfg.SlackWebhookURL != "" || cfg.DiscordWebhookURL != "" {
		t.Fatalf("operator channels must default to unconfigured: %+v", cfg)
	}
	if cfg.JWTSecret != "" || cfg.AllowInsecure || cfg.MetadataPolicy != config.MetadataPolicyAllow || cfg.LogLevel != "info" {
		t.Fatalf("unexpected shared block defaults: %+v", cfg)
	}
	if cfg.Support() != config.DefaultSupportEmail || cfg.Logo() != "" {
		t.Fatalf("branding defaults: support %q logo %q", cfg.Support(), cfg.Logo())
	}
}

func TestLookoutRequiredKeys(t *testing.T) {
	_, err := loadLookout(nil)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	want := []string{"DATABASE_URL", "SERVICE_TOKEN", "LOOKOUT_ALERTMANAGER_TOKEN", "QUARTERMASTER_GRPC_ADDR", "DECKLOG_GRPC_ADDR", "KAFKA_BROKERS"}
	got := slices.Clone(loadErr.Missing)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
}

func TestLookoutBlankRequiredValueCountsAsMissing(t *testing.T) {
	values := requiredLookout()
	values["LOOKOUT_ALERTMANAGER_TOKEN"] = "   "
	_, err := loadLookout(values)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) || !slices.Equal(loadErr.Missing, []string{"LOOKOUT_ALERTMANAGER_TOKEN"}) {
		t.Fatalf("Load error = %v, want LOOKOUT_ALERTMANAGER_TOKEN missing", err)
	}
}

func TestLookoutPortOverridesLookoutPort(t *testing.T) {
	values := requiredLookout()
	values["LOOKOUT_PORT"] = "28022"
	cfg, err := loadLookout(values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "28022" {
		t.Fatalf("LOOKOUT_PORT = %q", cfg.HTTPListenPort())
	}
	values["PORT"] = "38022"
	if cfg, err = loadLookout(values); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "38022" {
		t.Fatalf("PORT override = %q", cfg.HTTPListenPort())
	}
}

func TestLookoutNotificationSettings(t *testing.T) {
	values := requiredLookout()
	values["LOOKOUT_NOTIFY_EMAIL_TO"] = "oncall@example.test, ,lead@example.test"
	values["LOOKOUT_SLACK_WEBHOOK_URL"] = "https://hooks.slack.test/x"
	values["LOOKOUT_DISCORD_WEBHOOK_URL"] = "https://discord.test/x"
	values["SMTP_ALLOW_INSECURE"] = "true"
	values["WEBAPP_PUBLIC_URL"] = "https://app.example.test/"
	values["SUPPORT_EMAIL"] = "help@example.test"
	cfg, err := loadLookout(values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(cfg.NotifyEmailTo, []string{"oncall@example.test", "lead@example.test"}) {
		t.Fatalf("NotifyEmailTo = %v", cfg.NotifyEmailTo)
	}
	if cfg.SlackWebhookURL != "https://hooks.slack.test/x" || cfg.DiscordWebhookURL != "https://discord.test/x" || !cfg.SMTPAllowInsecure {
		t.Fatalf("unexpected channel settings: %+v", cfg)
	}
	if cfg.WebAppURL != "https://app.example.test/" || cfg.Support() != "help@example.test" {
		t.Fatalf("branding = %+v", cfg.EmailBranding)
	}
}

func TestLookoutWebhookURLsAreRedacted(t *testing.T) {
	values := requiredLookout()
	values["LOOKOUT_SLACK_WEBHOOK_URL"] = "https://hooks.slack.test/SECRET"
	values["LOOKOUT_DISCORD_WEBHOOK_URL"] = "https://discord.test/SECRET"
	opts := config.Options{Service: "lookout", Lookup: func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}}
	cfg, err := config.Load[Lookout](opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	described, err := config.Describe(cfg, opts)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	for _, field := range described {
		switch field.Key {
		case "LOOKOUT_SLACK_WEBHOOK_URL", "LOOKOUT_DISCORD_WEBHOOK_URL", "LOOKOUT_ALERTMANAGER_TOKEN", "SMTP_PASSWORD", "SERVICE_TOKEN", "DATABASE_URL":
			if field.Value != "" && field.Value != config.RedactedValue {
				t.Fatalf("%s is reported in clear text", field.Key)
			}
		}
	}
}

func TestLookoutRejectsInvalidBoolean(t *testing.T) {
	values := requiredLookout()
	values["SMTP_ALLOW_INSECURE"] = "sometimes"
	_, err := loadLookout(values)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) || len(loadErr.Invalid) != 1 {
		t.Fatalf("Load error = %v, want SMTP_ALLOW_INSECURE invalid", err)
	}
}
