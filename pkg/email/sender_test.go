package email

import (
	"strings"
	"testing"
)

func TestBuildHTMLMessageIncludesRequiredHeaders(t *testing.T) {
	msg := string(buildHTMLMessage(Config{
		From:     "noreply@frameworks.network",
		FromName: "FrameWorks",
	}, "user@example.com", "Verify your FrameWorks account", "<p>Hello</p>"))

	for _, want := range []string{
		"From: FrameWorks <noreply@frameworks.network>\r\n",
		"To: user@example.com\r\n",
		"Subject: Verify your FrameWorks account\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/html; charset=UTF-8\r\n",
		"\r\n<p>Hello</p>",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestBuildHTMLMessageSanitizesHeaderInjection(t *testing.T) {
	msg := string(buildHTMLMessage(Config{
		From:     "noreply@frameworks.network\r\nBcc: attacker@example.com",
		FromName: "FrameWorks\r\nX-Bad: yes",
	}, "user@example.com\r\nCc: attacker@example.com", "Verify\r\nX-Bad: yes", "<p>Hello</p>"))
	headers := strings.SplitN(msg, "\r\n\r\n", 2)[0]

	for _, blocked := range []string{"Bcc:", "Cc:", "X-Bad:"} {
		if strings.Contains(headers, blocked) {
			t.Fatalf("header injection %q was not sanitized:\n%s", blocked, headers)
		}
	}
}
