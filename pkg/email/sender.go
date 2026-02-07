package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
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

	fromHeader := s.config.From
	if strings.TrimSpace(s.config.FromName) != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.config.FromName, s.config.From)
	}

	fromHeader = sanitizeHeader(fromHeader)
	to = sanitizeHeader(to)
	subject = sanitizeHeader(subject)

	msg := []string{
		fmt.Sprintf("From: %s", fromHeader),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		htmlBody,
	}

	body := []byte(strings.Join(msg, "\r\n"))

	if s.auth != nil {
		return smtp.SendMail(addr, s.auth, s.config.From, []string{to}, body)
	}

	// No auth - connect directly
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer func() { _ = c.Close() }()

	if errMail := c.Mail(s.config.From); errMail != nil {
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

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}
