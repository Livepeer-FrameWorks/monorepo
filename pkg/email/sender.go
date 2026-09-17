package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	// AllowInsecure permits SMTP without STARTTLS for explicitly isolated test
	// or development relays. Production callers must leave this false.
	AllowInsecure bool
	// From is the SMTP envelope sender (MAIL FROM). This should be a raw mailbox address.
	From string
	// FromName is an optional display name used only for the message header.
	FromName string
}

type Sender struct {
	config Config
	auth   smtp.Auth
}

type Message struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
	ReplyTo  string
}

func NewSender(config Config) *Sender {
	config.FromName = normalizedFromName(config.FromName)
	var auth smtp.Auth
	if config.User != "" && config.Password != "" {
		auth = smtp.PlainAuth("", config.User, config.Password, config.Host)
	}

	return &Sender{
		config: config,
		auth:   auth,
	}
}

func normalizedFromName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "FrameWorks"
	}
	if address, err := mail.ParseAddress(value); err == nil && strings.EqualFold(address.Address, value) {
		return "FrameWorks"
	}
	return value
}

func (s *Sender) SendMail(ctx context.Context, to, subject, htmlBody string) error {
	return s.Send(ctx, Message{
		To:       to,
		Subject:  subject,
		TextBody: HTMLToText(htmlBody),
		HTMLBody: htmlBody,
	})
}

func (s *Sender) Send(ctx context.Context, message Message) error {
	addr := fmt.Sprintf("%s:%s", s.config.Host, s.config.Port)
	from := sanitizeHeader(s.config.From)
	to := sanitizeHeader(message.To)
	body, err := buildMessage(s.config, message)
	if err != nil {
		return fmt.Errorf("build email: %w", err)
	}

	dialer := net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
		return fmt.Errorf("set smtp deadline: %w", deadlineErr)
	}

	c, err := smtp.NewClient(conn, s.config.Host)
	if err != nil {
		return fmt.Errorf("initialize smtp: %w", err)
	}
	defer func() { _ = c.Close() }()

	cancelled := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-cancelled:
		}
	}()
	defer close(cancelled)

	if ok, _ := c.Extension("STARTTLS"); ok {
		if tlsErr := c.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); tlsErr != nil {
			return fmt.Errorf("start tls: %w", tlsErr)
		}
	} else if !s.config.AllowInsecure {
		return fmt.Errorf("smtp server does not advertise STARTTLS")
	}
	if s.auth != nil {
		if authErr := c.Auth(s.auth); authErr != nil {
			return fmt.Errorf("smtp auth: %w", authErr)
		}
	}

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

func buildMessage(config Config, message Message) ([]byte, error) {
	var buf bytes.Buffer
	boundary := multipart.NewWriter(&buf)
	if err := boundary.SetBoundary(messageBoundary()); err != nil {
		return nil, err
	}

	fromHeader := formatAddressHeader(config.FromName, config.From)
	toHeader := formatAddressHeader("", message.To)
	headers := []string{
		fmt.Sprintf("From: %s", fromHeader),
		fmt.Sprintf("To: %s", toHeader),
		fmt.Sprintf("Subject: %s", mime.QEncoding.Encode("UTF-8", sanitizeHeader(message.Subject))),
		fmt.Sprintf("Date: %s", time.Now().UTC().Format(time.RFC1123Z)),
		fmt.Sprintf("Message-ID: <%s@%s>", messageID(), messageIDDomain(config.From)),
		"MIME-Version: 1.0",
		fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q", boundary.Boundary()),
	}
	if replyTo := sanitizeHeader(message.ReplyTo); replyTo != "" {
		headers = append(headers, fmt.Sprintf("Reply-To: %s", formatAddressHeader("", replyTo)))
	}
	buf.WriteString(strings.Join(headers, "\r\n"))
	buf.WriteString("\r\n\r\n")

	textBody := strings.TrimSpace(message.TextBody)
	if textBody == "" {
		textBody = HTMLToText(message.HTMLBody)
	}
	if err := writeAlternativePart(boundary, "text/plain; charset=UTF-8", textBody); err != nil {
		return nil, err
	}
	if err := writeAlternativePart(boundary, "text/html; charset=UTF-8", message.HTMLBody); err != nil {
		return nil, err
	}
	if err := boundary.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeAlternativePart(writer *multipart.Writer, contentType, body string) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	encoded := quotedprintable.NewWriter(part)
	if _, err = encoded.Write([]byte(body)); err != nil {
		return err
	}
	return encoded.Close()
}

func messageBoundary() string {
	return "frameworks-" + randomHex(12)
}

func messageID() string {
	return fmt.Sprintf("%d.%s", time.Now().UTC().UnixNano(), randomHex(8))
}

func randomHex(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(b)
}

func messageIDDomain(from string) string {
	if address, err := mail.ParseAddress(sanitizeHeader(from)); err == nil {
		if _, domain, ok := strings.Cut(address.Address, "@"); ok && domain != "" {
			return domain
		}
	}
	return "frameworks.network"
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
