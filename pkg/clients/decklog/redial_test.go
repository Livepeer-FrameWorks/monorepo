package decklog

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// droppingListener accepts every connection and closes it at once, so each
// gRPC connection attempt fails before the HTTP/2 handshake, like a peer whose
// service has stopped. It counts the attempts.
func droppingListener(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var attempts atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			attempts.Add(1)
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), &attempts
}

// A client of a stopped Decklog must back off between redials: without a
// backoff it opens thousands of connections per second, and a same-host one
// that connects to itself holds Decklog's port so the restart cannot bind it.
func TestClientsBackOffWhileDecklogDropsConnections(t *testing.T) {
	const window = 2 * time.Second
	// Backoff of 1s, then 1.6s (±20%): at most 3 attempts fit in the window.
	const maxAttempts = 4
	cfg := func(target string) ClientConfig {
		return ClientConfig{Target: target, AllowInsecure: true, Timeout: time.Second}
	}

	t.Run("client", func(t *testing.T) {
		addr, attempts := droppingListener(t)
		client, err := NewClient(cfg(addr), logging.NewLogger())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		defer func() { _ = client.conn.Close() }()
		client.conn.Connect()
		time.Sleep(window)
		if got := attempts.Load(); got > maxAttempts {
			t.Fatalf("%d connection attempts in %s, want at most %d", got, window, maxAttempts)
		}
	})

	t.Run("batched client", func(t *testing.T) {
		addr, attempts := droppingListener(t)
		c := cfg(addr)
		client, err := NewBatchedClient(BatchedClientConfig{Target: c.Target, AllowInsecure: c.AllowInsecure, Timeout: c.Timeout}, logging.NewLogger())
		if err != nil {
			t.Fatalf("NewBatchedClient: %v", err)
		}
		defer func() { _ = client.conn.Close() }()
		client.conn.Connect()
		time.Sleep(window)
		if got := attempts.Load(); got > maxAttempts {
			t.Fatalf("%d connection attempts in %s, want at most %d", got, window, maxAttempts)
		}
	})
}
