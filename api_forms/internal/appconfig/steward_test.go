package appconfig

import (
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadSteward(t *testing.T, values map[string]string) (*Steward, error) {
	t.Helper()
	return config.Load[Steward](config.Options{Service: "steward", Lookup: func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}})
}

func TestStewardDefaults(t *testing.T) {
	cfg, err := loadSteward(t, map[string]string{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "18032" || cfg.SMTPPort != "587" || cfg.DefaultMailingListID != 1 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.FromEmail != "noreply@frameworks.network" || cfg.EmailSubjectPrefix != "Contact Form" {
		t.Errorf("unexpected email defaults: %+v", cfg)
	}
	if cfg.ContactSuccessMessage != "Thank you for your message! We'll get back to you soon." {
		t.Errorf("ContactSuccessMessage = %q", cfg.ContactSuccessMessage)
	}
	if cfg.ToEmail != "" || cfg.ContactRecipient() != DefaultContactRecipient {
		t.Errorf("ToEmail = %q, ContactRecipient = %q", cfg.ToEmail, cfg.ContactRecipient())
	}
}

func TestStewardContactRecipientUsesToEmail(t *testing.T) {
	cfg, err := loadSteward(t, map[string]string{"TO_EMAIL": "ops@example.com"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.ContactRecipient(); got != "ops@example.com" {
		t.Errorf("ContactRecipient = %q", got)
	}
}

func TestStewardRejectsNonIntegerMailingList(t *testing.T) {
	_, err := loadSteward(t, map[string]string{"DEFAULT_MAILING_LIST_ID": "newsletter"})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) || len(loadErr.Invalid) != 1 {
		t.Fatalf("Load error = %v, want DEFAULT_MAILING_LIST_ID invalid", err)
	}
}

func TestStewardEmailBranding(t *testing.T) {
	cfg, err := loadSteward(t, map[string]string{
		"EMAIL_LOGO_URL":    "https://cdn.example.test/logo.png",
		"WEBAPP_PUBLIC_URL": "https://app.example.test",
		"SUPPORT_EMAIL":     "help@example.test",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.EmailBranding{LogoURL: "https://cdn.example.test/logo.png", WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}
	if cfg.EmailBranding != want {
		t.Errorf("EmailBranding = %+v, want %+v", cfg.EmailBranding, want)
	}
}
