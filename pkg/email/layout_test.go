package email

import (
	"html/template"
	"strings"
	"testing"
)

func TestRenderLayoutUsesBrandIdentityAndEscapesContent(t *testing.T) {
	body, err := RenderLayout(LayoutData{
		LogoURL:      "https://app.example.test/frameworks-light-logomark.png",
		Preheader:    "Confirm your address",
		Eyebrow:      "Account setup",
		Title:        "Verify your email address",
		SupportEmail: "support@example.test",
		Content: struct {
			Name   string
			Action Action
		}{
			Name:   `<script>alert("x")</script>`,
			Action: Action{URL: "https://app.example.test/verify?token=abc", Label: "Verify email address"},
		},
	}, `<p style="margin:0 0 16px;">Hello {{.Name}}</p>{{template "action" .Action}}`, template.FuncMap{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"FrameWorks",
		"Network",
		"#0f4b6e",
		"Account setup",
		"Verify your email address",
		"Verify email address",
		"support@example.test",
		"&lt;script&gt;alert",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered email missing %q", want)
		}
	}
	if strings.Contains(body, `<script>alert`) {
		t.Fatal("dynamic content was not escaped")
	}
}

func TestPublicLogoURL(t *testing.T) {
	if got := PublicLogoURL("https://cdn.example.test/logo.png", "https://app.example.test"); got != "https://cdn.example.test/logo.png" {
		t.Fatalf("configured logo URL = %q", got)
	}
	if got := PublicLogoURL("", "https://app.example.test/"); got != "https://app.example.test/frameworks-light-logomark.png" {
		t.Fatalf("derived logo URL = %q", got)
	}
}

func TestHTMLToTextKeepsReadableParagraphs(t *testing.T) {
	got := HTMLToText(`<h1>Verify your email</h1><p>Welcome to <strong>FrameWorks</strong>.</p><p><a href="https://example.test">Verify now</a></p>`)
	for _, want := range []string{"Verify your email", "Welcome to FrameWorks.", "Verify now (https://example.test)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain text %q missing %q", got, want)
		}
	}
}

func TestHTMLToTextSkipsHiddenMetadataAndSeparatesCells(t *testing.T) {
	got := HTMLToText(`<html><head><title>Duplicate title</title><style>.x{}</style></head><body><div style="display:none; max-height:0">Hidden preheader</div><table><tr><th>Status</th><td>Healthy</td></tr></table></body></html>`)
	if strings.Contains(got, "Duplicate title") || strings.Contains(got, "Hidden preheader") || strings.Contains(got, ".x") {
		t.Fatalf("plain text contains hidden metadata: %q", got)
	}
	if !strings.Contains(got, "Status\tHealthy") {
		t.Fatalf("table cells were not separated: %q", got)
	}
}
