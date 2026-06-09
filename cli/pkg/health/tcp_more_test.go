package health

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestTCPChecker_ZeroTimeoutStillConnects(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	// Timeout==0 → default applied; a live local listener must still connect.
	checker := &TCPChecker{Timeout: 0}
	result := checker.Check("127.0.0.1", port)
	if !result.OK || result.Status != "healthy" {
		t.Fatalf("zero-timeout checker must connect to live listener; got %#v", result)
	}
	want := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if result.Metadata["address"] != want {
		t.Fatalf("address metadata = %q, want %q", result.Metadata["address"], want)
	}
}

func TestTCPChecker_ExplicitTimeoutPreserved(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	checker := &TCPChecker{Timeout: 2 * time.Second}
	result := checker.Check("127.0.0.1", port)
	if !result.OK {
		t.Fatalf("explicit-timeout checker must connect; got %#v", result)
	}
	if result.Latency <= 0 {
		t.Fatalf("latency must be measured; got %v", result.Latency)
	}
}
