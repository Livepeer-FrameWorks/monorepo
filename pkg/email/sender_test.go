package email

import (
	"bufio"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestBuildMessageIncludesRequiredHeaders(t *testing.T) {
	data, err := buildMessage(Config{
		From:     "noreply@frameworks.network",
		FromName: "FrameWorks",
	}, Message{To: "user@example.com", Subject: "Verify your FrameWorks account", HTMLBody: "<p>Hello</p>"})
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)

	for _, want := range []string{
		"From: \"FrameWorks\" <noreply@frameworks.network>\r\n",
		"To: <user@example.com>\r\n",
		"Subject: Verify your FrameWorks account\r\n",
		"Date: ",
		"MIME-Version: 1.0\r\n",
		"Content-Type: multipart/alternative;",
		"Content-Transfer-Encoding: quoted-printable\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestBuildMessageIncludesPlainTextAndHTMLAlternatives(t *testing.T) {
	data, err := buildMessage(Config{
		From:     "noreply@frameworks.network",
		FromName: "FrameWorks",
	}, Message{
		To:       "user@example.com",
		Subject:  "Verify your FrameWorks account",
		TextBody: "Verify at https://app.example.test/verify",
		HTMLBody: `<p>Verify at <a href="https://app.example.test/verify">FrameWorks</a></p>`,
		ReplyTo:  "support@frameworks.network",
	})
	if err != nil {
		t.Fatal(err)
	}

	message, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got := message.Header.Get("Reply-To"); got != "<support@frameworks.network>" {
		t.Fatalf("Reply-To = %q", got)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q", mediaType)
	}

	reader := multipart.NewReader(message.Body, params["boundary"])
	parts := map[string]string{}
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			t.Fatal(partErr)
		}
		body, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Fatal(readErr)
		}
		parts[part.Header.Get("Content-Type")] = string(body)
	}
	if !strings.Contains(parts["text/plain; charset=UTF-8"], "Verify at https://") {
		t.Fatalf("plain-text part = %q", parts["text/plain; charset=UTF-8"])
	}
	if !strings.Contains(parts["text/html; charset=UTF-8"], "<a href=") {
		t.Fatalf("HTML part = %q", parts["text/html; charset=UTF-8"])
	}
}

func TestBuildMessageUsesSevenBitSafeEncodingAndLineLengths(t *testing.T) {
	data, err := buildMessage(Config{From: "noreply@frameworks.network"}, Message{
		To:       "user@example.com",
		Subject:  "Unicode delivery",
		TextBody: strings.Repeat("FrameWorks · café ", 100),
		HTMLBody: `<p>` + strings.Repeat("FrameWorks · café ", 100) + `</p>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "café") || !strings.Contains(string(data), "=C2=B7") {
		t.Fatalf("message was not encoded as seven-bit quoted-printable")
	}
	for _, line := range strings.Split(string(data), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("SMTP line is %d octets, want <= 998", len(line))
		}
	}
}

func TestBuildMessageQuotesSpecialFromName(t *testing.T) {
	data, err := buildMessage(Config{
		From:     "noreply@frameworks.network",
		FromName: "FrameWorks, Inc.",
	}, Message{To: "user@example.com", Subject: "Verify your FrameWorks account", HTMLBody: "<p>Hello</p>"})
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)

	if !strings.Contains(msg, "From: \"FrameWorks, Inc.\" <noreply@frameworks.network>\r\n") {
		t.Fatalf("message did not quote display name:\n%s", msg)
	}
}

func TestBuildMessageSanitizesHeaderInjection(t *testing.T) {
	data, err := buildMessage(Config{
		From:     "noreply@frameworks.network\r\nBcc: attacker@example.com",
		FromName: "FrameWorks\r\nX-Bad: yes",
	}, Message{
		To:       "user@example.com\r\nCc: attacker@example.com",
		Subject:  "Verify\r\nX-Bad: yes",
		HTMLBody: "<p>Hello</p>",
		ReplyTo:  "support@example.com\r\nBcc: attacker@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	headers := strings.SplitN(msg, "\r\n\r\n", 2)[0]

	for _, blocked := range []string{"Bcc:", "Cc:", "X-Bad:"} {
		if strings.Contains(headers, blocked) {
			t.Fatalf("header injection %q was not sanitized:\n%s", blocked, headers)
		}
	}
}

func TestSenderRequiresSTARTTLSByDefault(t *testing.T) {
	host, port, _ := startTestSMTPServer(t, false)
	sender := NewSender(Config{Host: host, Port: port, From: "noreply@frameworks.network"})
	err := sender.Send(context.Background(), Message{To: "user@example.com", Subject: "Test", HTMLBody: "<p>Hello</p>"})
	if err == nil || !strings.Contains(err.Error(), "does not advertise STARTTLS") {
		t.Fatalf("Send error = %v, want missing STARTTLS failure", err)
	}
}

func TestSenderFailsWhenSTARTTLSUpgradeFails(t *testing.T) {
	host, port, _ := startTestSMTPServer(t, true)
	sender := NewSender(Config{Host: host, Port: port, From: "noreply@frameworks.network"})
	err := sender.Send(context.Background(), Message{To: "user@example.com", Subject: "Test", HTMLBody: "<p>Hello</p>"})
	if err == nil || !strings.Contains(err.Error(), "start tls") {
		t.Fatalf("Send error = %v, want STARTTLS upgrade failure", err)
	}
}

func TestSenderAllowsExplicitInsecureDevelopmentRelay(t *testing.T) {
	host, port, delivered := startTestSMTPServer(t, false)
	sender := NewSender(Config{
		Host: host, Port: port, From: "noreply@frameworks.network", AllowInsecure: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sender.Send(ctx, Message{To: "user@example.com", Subject: "Test", HTMLBody: "<p>Hello</p>"}); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-delivered:
		if !strings.Contains(message, "Content-Transfer-Encoding: quoted-printable") {
			t.Fatalf("delivered message did not use quoted-printable: %q", message)
		}
	case <-ctx.Done():
		t.Fatal("SMTP server did not receive message")
	}
}

func startTestSMTPServer(t *testing.T, advertiseSTARTTLS bool) (string, string, <-chan string) {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	delivered := make(chan string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		_, _ = writer.WriteString("220 localhost ESMTP\r\n")
		_ = writer.Flush()
		var message strings.Builder
		inData := false
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if inData {
				if trimmed == "." {
					delivered <- message.String()
					inData = false
					_, _ = writer.WriteString("250 queued\r\n")
					_ = writer.Flush()
					continue
				}
				message.WriteString(line)
				continue
			}
			switch {
			case strings.HasPrefix(trimmed, "EHLO"):
				if advertiseSTARTTLS {
					_, _ = writer.WriteString("250-localhost\r\n250 STARTTLS\r\n")
				} else {
					_, _ = writer.WriteString("250 localhost\r\n")
				}
			case strings.HasPrefix(trimmed, "MAIL FROM"), strings.HasPrefix(trimmed, "RCPT TO"):
				_, _ = writer.WriteString("250 ok\r\n")
			case trimmed == "DATA":
				inData = true
				_, _ = writer.WriteString("354 send data\r\n")
			case trimmed == "QUIT":
				_, _ = writer.WriteString("221 bye\r\n")
				_ = writer.Flush()
				return
			default:
				_, _ = writer.WriteString("250 ok\r\n")
			}
			_ = writer.Flush()
		}
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host, port, delivered
}

func TestNewSenderDefaultsDisplayName(t *testing.T) {
	sender := NewSender(Config{From: "noreply@frameworks.network"})
	if sender.config.FromName != "FrameWorks" {
		t.Fatalf("FromName = %q, want FrameWorks", sender.config.FromName)
	}
}

func TestNewSenderRejectsMailboxAsDisplayName(t *testing.T) {
	sender := NewSender(Config{From: "noreply@frameworks.network", FromName: "info@frameworks.network"})
	if sender.config.FromName != "FrameWorks" {
		t.Fatalf("FromName = %q, want FrameWorks", sender.config.FromName)
	}
}
