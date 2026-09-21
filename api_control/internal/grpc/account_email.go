package grpc

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"

	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
)

type accountEmailContent struct {
	Intro       string
	Action      emailpkg.Action
	Notice      emailpkg.Notice
	FallbackURL string
}

const accountEmailContentTemplate = `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{.Intro}}</p>
{{template "action" .Action}}
{{template "notice" .Notice}}
{{template "fallbackURL" .FallbackURL}}`

func (r RuntimeSettings) renderVerificationEmail(baseURL, token string) (emailpkg.Message, error) {
	verifyURL := strings.TrimRight(baseURL, "/") + "/verify-email#token=" + url.QueryEscape(token)
	content := accountEmailContent{
		Intro:  "Welcome to FrameWorks. Confirm that this email belongs to you to finish creating your account.",
		Action: emailpkg.Action{URL: verifyURL, Label: "Verify email address"},
		Notice: emailpkg.Notice{
			Title: "This link expires in 24 hours.",
			Text:  "If you did not create a FrameWorks account, no action is required.",
		},
		FallbackURL: verifyURL,
	}
	htmlBody, err := emailpkg.RenderLayout(r.accountEmailLayout(
		"Confirm your email address to finish setting up FrameWorks.",
		"Account setup",
		"Verify your email address",
		content,
	), accountEmailContentTemplate, template.FuncMap{})
	if err != nil {
		return emailpkg.Message{}, fmt.Errorf("render verification email: %w", err)
	}
	return emailpkg.Message{
		Subject:  "Verify your email to finish setting up FrameWorks",
		HTMLBody: htmlBody,
		TextBody: emailpkg.PlainTextAction(content.Intro, content.Action, content.Notice, content.FallbackURL),
		ReplyTo:  r.Branding.Support(),
	}, nil
}

func (r RuntimeSettings) renderPasswordResetEmail(baseURL, token string) (emailpkg.Message, error) {
	resetURL := strings.TrimRight(baseURL, "/") + "/reset-password#token=" + url.QueryEscape(token)
	content := accountEmailContent{
		Intro:  "We received a request to choose a new password for your FrameWorks account.",
		Action: emailpkg.Action{URL: resetURL, Label: "Reset password"},
		Notice: emailpkg.Notice{
			Title: "This link expires in 1 hour.",
			Text:  "If you did not request a reset, you can ignore this email. Your password will not change.",
		},
		FallbackURL: resetURL,
	}
	htmlBody, err := emailpkg.RenderLayout(r.accountEmailLayout(
		"Use this secure link to reset your FrameWorks password.",
		"Account security",
		"Reset your password",
		content,
	), accountEmailContentTemplate, template.FuncMap{})
	if err != nil {
		return emailpkg.Message{}, fmt.Errorf("render password reset email: %w", err)
	}
	return emailpkg.Message{
		Subject:  "Reset your FrameWorks password",
		HTMLBody: htmlBody,
		TextBody: emailpkg.PlainTextAction(content.Intro, content.Action, content.Notice, content.FallbackURL),
		ReplyTo:  r.Branding.Support(),
	}, nil
}

func validatedAccountEmailBaseURL(rawURL string, development bool) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || !walletChallengeOriginAllowed(parsedURL, development) {
		return "", fmt.Errorf("WEBAPP_PUBLIC_URL must be an absolute HTTPS URL (HTTP loopback is allowed in development)")
	}
	return baseURL, nil
}

func (r RuntimeSettings) accountEmailLayout(preheader, eyebrow, title string, content accountEmailContent) emailpkg.LayoutData {
	return emailpkg.LayoutData{
		LogoURL:      r.Branding.Logo(),
		Preheader:    preheader,
		Eyebrow:      eyebrow,
		Title:        title,
		SupportEmail: r.Branding.Support(),
		Content:      content,
	}
}
