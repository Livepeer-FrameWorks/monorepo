package main

import (
	"reflect"
	"testing"

	"frameworks/api_incidents/internal/appconfig"
	"frameworks/api_incidents/internal/notify"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
)

func lookoutConfig(t *testing.T, values map[string]string) (*config.Live[appconfig.Lookout], map[string]string) {
	t.Helper()
	env := map[string]string{
		"DATABASE_URL":               "postgres://lookout@db:5432/lookout",
		"SERVICE_TOKEN":              "token",
		"QUARTERMASTER_GRPC_ADDR":    "quartermaster:19002",
		"DECKLOG_GRPC_ADDR":          "decklog:18006",
		"KAFKA_BROKERS":              "kafka:9092",
		"LOOKOUT_ALERTMANAGER_TOKEN": "alertmanager-token",
	}
	for key, value := range values {
		env[key] = value
	}
	opts := config.Options{Service: "lookout", Lookup: func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}}
	cfg, err := config.Load[appconfig.Lookout](opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return config.NewLive(cfg, opts), env
}

func TestNotifySettingsMapsConfiguration(t *testing.T) {
	live, _ := lookoutConfig(t, map[string]string{
		"LOOKOUT_NOTIFY_EMAIL_TO":     "oncall@example.test, lead@example.test",
		"LOOKOUT_SLACK_WEBHOOK_URL":   "https://hooks.slack.test/x",
		"LOOKOUT_DISCORD_WEBHOOK_URL": "https://discord.test/x",
		"WEBAPP_PUBLIC_URL":           "https://app.example.test/",
		"EMAIL_LOGO_URL":              "https://cdn.example.test/logo.png",
		"SUPPORT_EMAIL":               "help@example.test",
		"SMTP_HOST":                   "smtp.example.test",
		"SMTP_USER":                   "mailer",
		"SMTP_PASSWORD":               "secret",
		"SMTP_ALLOW_INSECURE":         "true",
	})
	want := notify.Settings{
		EmailRecipients:   []string{"oncall@example.test", "lead@example.test"},
		SlackWebhookURL:   "https://hooks.slack.test/x",
		DiscordWebhookURL: "https://discord.test/x",
		WebappURL:         "https://app.example.test",
		SMTP: email.Config{
			Host:          "smtp.example.test",
			Port:          "587",
			User:          "mailer",
			Password:      "secret",
			From:          "noreply@frameworks.network",
			FromName:      "FrameWorks",
			AllowInsecure: true,
		},
		Branding: config.EmailBranding{
			LogoURL:      "https://cdn.example.test/logo.png",
			WebAppURL:    "https://app.example.test/",
			SupportEmail: "help@example.test",
		},
	}
	if got := notifySettings(live.Get()); !reflect.DeepEqual(got, want) {
		t.Fatalf("notifySettings = %+v, want %+v", got, want)
	}
}

func TestNotifySettingsFollowReload(t *testing.T) {
	live, env := lookoutConfig(t, nil)
	source := notify.SettingsSource(func() notify.Settings { return notifySettings(live.Get()) })
	router := notify.Router{Settings: source}
	if router.Enabled("slack") {
		t.Fatal("slack enabled before a webhook URL is configured")
	}

	env["LOOKOUT_SLACK_WEBHOOK_URL"] = "https://hooks.slack.test/x"
	if err := live.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !router.Enabled("slack") {
		t.Fatal("slack still disabled after the reload configured it")
	}

	delete(env, "LOOKOUT_ALERTMANAGER_TOKEN")
	delete(env, "LOOKOUT_SLACK_WEBHOOK_URL")
	if err := live.Reload(); err == nil {
		t.Fatal("reload without LOOKOUT_ALERTMANAGER_TOKEN must fail")
	}
	if !router.Enabled("slack") || live.Get().AlertmanagerToken != "alertmanager-token" {
		t.Fatal("a failed reload must keep the previous configuration")
	}
}
