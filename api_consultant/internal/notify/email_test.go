package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/email"
	"frameworks/pkg/logging"
)

type smtpCapture struct {
	addr string
	rcpt string
	data string
	done chan struct{}
}

func startSMTPServer(t *testing.T) (*smtpCapture, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen smtp: %v", err)
	}

	capture := &smtpCapture{
		addr: listener.Addr().String(),
		done: make(chan struct{}),
	}

	go func() {
		defer close(capture.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		writer := bufio.NewWriter(conn)
		reader := bufio.NewReader(conn)

		writeLine := func(line string) {
			_, _ = writer.WriteString(line + "\r\n")
			_ = writer.Flush()
		}

		writeLine("220 localhost")

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			upper := strings.ToUpper(line)

			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				writeLine("250-localhost")
				writeLine("250 OK")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				writeLine("250 OK")
			case strings.HasPrefix(upper, "RCPT TO:"):
				capture.rcpt = strings.TrimSpace(line[len("RCPT TO:"):])
				writeLine("250 OK")
			case strings.HasPrefix(upper, "DATA"):
				writeLine("354 End data with <CR><LF>.<CR><LF>")
				var dataLines []string
				for {
					dataLine, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					dataLine = strings.TrimRight(dataLine, "\r\n")
					if dataLine == "." {
						break
					}
					dataLines = append(dataLines, dataLine)
				}
				capture.data = strings.Join(dataLines, "\n")
				writeLine("250 OK")
			case strings.HasPrefix(upper, "QUIT"):
				writeLine("221 Bye")
				return
			default:
				writeLine("250 OK")
			}
		}
	}()

	return capture, func() { _ = listener.Close() }
}

func TestEmailNotifierSendsToBillingEmail(t *testing.T) {
	capture, stop := startSMTPServer(t)
	defer stop()

	host, port, err := net.SplitHostPort(capture.addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	notifier := NewEmailNotifier(Config{
		SMTP: email.Config{
			Host: host,
			Port: port,
			From: "noreply@example.com",
		},
	}, logging.NewLoggerWithService("skipper-test"))

	report := Report{
		TenantID:       "tenant-a",
		RecipientEmail: "billing@example.com",
		Summary:        "Summary",
		GeneratedAt:    time.Now().UTC(),
	}

	if err := notifier.Notify(context.Background(), report); err != nil {
		t.Fatalf("notify: %v", err)
	}

	select {
	case <-capture.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for smtp capture")
	}

	if !strings.Contains(strings.ToLower(capture.rcpt), "billing@example.com") {
		t.Fatalf("expected rcpt billing@example.com, got %q", capture.rcpt)
	}
	if !strings.Contains(capture.data, "Skipper Investigation Report") {
		t.Fatalf("expected email body to include report header")
	}
}
