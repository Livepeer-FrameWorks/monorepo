package email

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// startRcptReplySMTPServer answers every command with 250 except RCPT TO,
// which gets rcptReply.
func startRcptReplySMTPServer(t *testing.T, rcptReply string) (string, string) {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
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
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			switch trimmed := strings.TrimRight(line, "\r\n"); {
			case strings.HasPrefix(trimmed, "RCPT TO"):
				_, _ = writer.WriteString(rcptReply + "\r\n")
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
	return host, port
}

func TestIsRecipientRejectedClassifiesRcptReplies(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  bool
	}{
		{"550 5.1.1 <nobody@example.com>: Recipient address rejected: User unknown", true},
		{"550 5.2.1 Mailbox disabled", true},
		{"553 mailbox name not allowed", true},
		{"554 5.7.1 Relay access denied", false},
		{"550 5.7.1 Sender not authorised", false},
		{"451 4.3.0 Temporary lookup failure", false},
		{"452 4.2.2 Mailbox full", false},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			host, port := startRcptReplySMTPServer(t, tc.reply)
			sender := NewSender(Config{Host: host, Port: port, From: "noreply@frameworks.network", AllowInsecure: true})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := sender.Send(ctx, Message{To: "nobody@example.com", Subject: "Test", HTMLBody: "<p>Hi</p>"})
			if err == nil {
				t.Fatal("expected the RCPT refusal to fail the send")
			}
			if got := IsRecipientRejected(err); got != tc.want {
				t.Fatalf("IsRecipientRejected(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}
	if IsRecipientRejected(errors.New("550 5.1.1 User unknown")) {
		t.Fatal("an error that is not an RCPT TO reply must not be classified as a recipient rejection")
	}
}
