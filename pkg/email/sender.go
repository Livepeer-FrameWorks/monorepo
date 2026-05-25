package email

import (
	"context"
	"fmt"
	"mime"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	// From is the SMTP envelope sender (MAIL FROM). This should be a raw mailbox address.
	From string
	// FromName is an optional display name used only for the message header.
	FromName string
}

type Sender struct {
	config Config
	auth   smtp.Auth
}

func NewSender(config Config) *Sender {
	var auth smtp.Auth
	if config.User != "" && config.Password != "" {
		auth = smtp.PlainAuth("", config.User, config.Password, config.Host)
	}

	return &Sender{
		config: config,
		auth:   auth,
	}
}

func (s *Sender) SendMail(ctx context.Context, to, subject, htmlBody string) error {
	addr := fmt.Sprintf("%s:%s", s.config.Host, s.config.Port)
	from := sanitizeHeader(s.config.From)
	to = sanitizeHeader(to)
	body := buildHTMLMessage(s.config, to, subject, htmlBody)

	if s.auth != nil {
		return smtp.SendMail(addr, s.auth, from, []string{to}, body)
	}

	// No auth - connect directly
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer func() { _ = c.Close() }()

	if errMail := c.Mail(from); errMail != nil {
		return fmt.Errorf("mail from: %w", errMail)
	}

	if errRcpt := c.Rcpt(to); errRcpt != nil {
		return fmt.Errorf("rcpt to: %w", errRcpt)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}

	_, err = w.Write(body)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return c.Quit()
}

func buildHTMLMessage(config Config, to, subject, htmlBody string) []byte {
	fromHeader := formatAddressHeader(config.FromName, config.From)
	toHeader := formatAddressHeader("", to)

	msg := []string{
		fmt.Sprintf("From: %s", fromHeader),
		fmt.Sprintf("To: %s", toHeader),
		fmt.Sprintf("Subject: %s", mime.QEncoding.Encode("UTF-8", sanitizeHeader(subject))),
		fmt.Sprintf("Date: %s", time.Now().UTC().Format(time.RFC1123Z)),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		htmlBody,
	}

	return []byte(strings.Join(msg, "\r\n") + "\r\n")
}

func sanitizeHeader(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func formatAddressHeader(displayName, address string) string {
	addr := mail.Address{
		Name:    sanitizeHeader(displayName),
		Address: sanitizeHeader(address),
	}
	return addr.String()
}
