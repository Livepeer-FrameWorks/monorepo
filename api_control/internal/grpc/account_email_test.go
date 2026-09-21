package grpc

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func TestRenderVerificationEmailUsesBrandLayoutAndEscapedToken(t *testing.T) {
	settings := RuntimeSettings{Branding: config.EmailBranding{WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}}
	message, err := settings.renderVerificationEmail("https://app.example.test/", "token with + symbols")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Verify your email to finish setting up FrameWorks",
		"frameworks-light-logomark.png",
		"Account setup",
		"Verify email address",
		"token+with+%2B+symbols",
		"This link expires in 24 hours.",
		"help@example.test",
	} {
		if !strings.Contains(message.Subject+message.HTMLBody+message.TextBody+message.ReplyTo, want) {
			t.Errorf("verification email missing %q", want)
		}
	}
}

func TestRenderPasswordResetEmailIncludesSecurityCopy(t *testing.T) {
	settings := RuntimeSettings{Branding: config.EmailBranding{WebAppURL: "https://app.example.test"}}
	message, err := settings.renderPasswordResetEmail("https://app.example.test", "reset-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Account security",
		"Reset password",
		"reset-password#token=reset-token",
		"This link expires in 1 hour.",
		"Your password will not change.",
	} {
		if !strings.Contains(message.HTMLBody+message.TextBody, want) {
			t.Errorf("password reset email missing %q", want)
		}
	}
}

func TestValidatedAccountEmailBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "https", value: " https://app.example.test/app/ ", want: "https://app.example.test/app"},
		{name: "http public", value: "http://app.example.test", wantErr: true},
		{name: "relative", value: "/app", wantErr: true},
		{name: "userinfo", value: "https://user@app.example.test", wantErr: true},
		{name: "query", value: "https://app.example.test/app?redirect=evil", wantErr: true},
		{name: "fragment", value: "https://app.example.test/app#other", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validatedAccountEmailBaseURL(test.value, false)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("URL = %q, want %q", got, test.want)
			}
		})
	}
}
