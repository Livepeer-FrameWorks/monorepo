package latencyproxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestRoundTripDelayAndCloseWithConnectedClient(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer connection.Close()
			_, _ = io.Copy(connection, connection)
		}
	}()
	p, err := Start(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	client, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", p.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	p.SetRoundTrip(110 * time.Millisecond)
	started := time.Now()
	if _, err = client.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(client, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 110*time.Millisecond {
		t.Fatalf("measured RTT %s is below injected 110ms", elapsed)
	}
	closed := make(chan struct{})
	go func() { _ = p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("closing a proxy with an idle connected client did not terminate")
	}
}
