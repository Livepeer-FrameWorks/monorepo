package grpc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc/peer"
)

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func TestExtractClientIP(t *testing.T) {
	t.Run("no peer in context", func(t *testing.T) {
		if got := extractClientIP(context.Background()); got != "" {
			t.Fatalf("expected empty client IP, got %q", got)
		}
	})

	t.Run("host:port peer address", func(t *testing.T) {
		ctx := peer.NewContext(context.Background(), &peer.Peer{
			Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443},
		})
		if got := extractClientIP(ctx); got != "203.0.113.7" {
			t.Fatalf("expected host extracted from host:port, got %q", got)
		}
	})

	t.Run("non host:port peer address", func(t *testing.T) {
		ctx := peer.NewContext(context.Background(), &peer.Peer{
			Addr: fakeAddr("upstream-proxy"),
		})
		if got := extractClientIP(ctx); got != "upstream-proxy" {
			t.Fatalf("expected raw addr string fallback, got %q", got)
		}
	})
}
