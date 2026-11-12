package grpc

import (
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func TestSessionSecurityEmailEscapesDeviceAndKeepsRequiredInformation(t *testing.T) {
	settings := RuntimeSettings{Branding: config.EmailBranding{WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}}
	for _, kind := range []string{"sign_in", "reuse"} {
		t.Run(kind, func(t *testing.T) {
			message, err := settings.renderSessionSecurityEmail("https://app.example.test", sessionSecurityEmail{Kind: kind, Device: `<script>alert("x")</script>`, ClientID: "cli", IP: "203.0.113.7", Location: "Amsterdam, Netherlands", Attribution: "IP Geolocation by DB-IP (https://db-ip.com)", At: time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"203.0.113.7", "Amsterdam, Netherlands", "2026-11-12T13:00:00Z", "https://app.example.test/account/security", "IP Geolocation by DB-IP"} {
				if !strings.Contains(message.HTMLBody, want) || !strings.Contains(message.TextBody, want) {
					t.Errorf("missing required email information %q", want)
				}
			}
			if strings.Contains(message.HTMLBody, "<script>") || !strings.Contains(message.HTMLBody, "&lt;script&gt;") {
				t.Fatal("device label became email markup")
			}
			if strings.Contains(message.HTMLBody, "unsubscribe") {
				t.Fatal("mandatory security alert has an opt-out")
			}
			if kind == "reuse" && !strings.Contains(message.TextBody, "We ended the affected session") {
				t.Fatal("theft notice missing session outcome")
			}
		})
	}
}

func TestBrowserSessionLabelOmitsVersions(t *testing.T) {
	for _, tc := range []struct{ agent, want string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0 Safari/537.36 Edg/131.0", "Edge on Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15", "Safari on macOS"},
		{"Mozilla/5.0 (X11; Linux x86_64) Firefox/132.0", "Firefox on Linux"},
		{"unknown agent", "Browser"},
	} {
		if got := browserSessionLabel(tc.agent); got != tc.want {
			t.Errorf("label=%q want=%q", got, tc.want)
		}
	}
	if sessionDeviceKey("cli", "same") == sessionDeviceKey("web", "same") {
		t.Fatal("device identity has an ambiguous boundary")
	}
}
