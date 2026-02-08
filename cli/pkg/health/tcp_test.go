package health

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTCPChecker(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	checker := &TCPChecker{Timeout: 200 * time.Millisecond}
	result := checker.Check("127.0.0.1", port)
	if !result.OK {
		t.Fatalf("expected TCP check to pass, got %#v", result)
	}

	_ = listener.Close()

	result = checker.Check("127.0.0.1", port)
	if result.OK {
		t.Fatalf("expected TCP check to fail after close, got %#v", result)
	}
}
