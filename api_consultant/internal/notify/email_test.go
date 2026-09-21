package notify

import (
	"bufio"
	"context"
	"io"
	"mime/quotedprintable"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
		WebAppURL: "https://old.example/app",
		BrandingSource: func() config.EmailBranding {
			return config.EmailBranding{WebAppURL: "https://new.example/app"}
		},
		SMTP: email.Config{
			Host:          host,
			Port:          port,
			From:          "noreply@example.com",
			AllowInsecure: true,
		},
	}, logging.NewLoggerWithService("skipper-test"))

	report := Report{
		InvestigationID: "inv-1",
		TenantID:        "tenant-a",
		RecipientEmail:  "billing@example.com",
		Summary:         "Summary",
		GeneratedAt:     time.Now().UTC(),
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
	body, decodeErr := io.ReadAll(quotedprintable.NewReader(strings.NewReader(capture.data)))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !strings.Contains(string(body), "https://new.example/app/skipper?report=inv-1") || strings.Contains(string(body), "https://old.example") {
		t.Fatal("report URL did not use the current branding snapshot")
	}
}

func TestRenderTemplateUsesBrandLayoutAndEscapesContent(t *testing.T) {
	notifier := &EmailNotifier{
		webAppURL: "https://app.example.test/app",
		branding:  config.EmailBranding{WebAppURL: "https://app.example.test/app"},
	}
	body, err := notifier.renderTemplate(emailReportData{
		TenantName:  `<script>alert("x")</script>`,
		Summary:     "Streams are healthy",
		ReportURL:   "https://app.example.test/app/skipper?report=inv-1",
		GeneratedAt: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FrameWorks", "#0f4b6e", "Investigation report", "View full report", "&lt;script&gt;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("email missing %q", want)
		}
	}
	if strings.Contains(body, `<script>`) {
		t.Fatalf("tenant name was not escaped: %s", body)
	}
}

func TestEmailBrandingReadsTheCurrentSnapshot(t *testing.T) {
	branding := config.EmailBranding{LogoURL: "https://old.example/logo.png"}
	notifier := NewEmailNotifier(Config{BrandingSource: func() config.EmailBranding { return branding }}, logging.NewLogger())
	before, err := notifier.renderTemplate(emailReportData{})
	if err != nil || !strings.Contains(before, branding.LogoURL) {
		t.Fatalf("initial branding: %v", err)
	}
	branding.LogoURL = "https://new.example/logo.png"
	after, err := notifier.renderTemplate(emailReportData{})
	if err != nil || !strings.Contains(after, branding.LogoURL) || strings.Contains(after, "https://old.example/logo.png") {
		t.Fatalf("reloaded branding: %v", err)
	}
}
