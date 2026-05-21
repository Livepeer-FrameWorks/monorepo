package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
)

// scriptedProbe returns a ProbeFunc that walks the steps slice on each
// call, returning the indexed step. Once exhausted, it returns the last
// step forever — convenient for "fails N times, then succeeds" or
// "always fails" scenarios.
type probeStep struct {
	stdout   string
	exitCode int
	err      error
}

func scriptedProbe(steps []probeStep, calls *atomic.Int32) ProbeFunc {
	return func(_ context.Context, _ inventory.Host, _ string) (ProbeResult, error) {
		idx := int(calls.Add(1)) - 1
		if idx >= len(steps) {
			idx = len(steps) - 1
		}
		s := steps[idx]
		if s.err != nil {
			return ProbeResult{}, s.err
		}
		return ProbeResult{Stdout: s.stdout, ExitCode: s.exitCode}, nil
	}
}

func TestHTTPReady_SucceedsImmediately(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{{stdout: "200", exitCode: 0}}, &calls)
	g := HTTPReady{Port: 18008, Path: "/health", Poll: 10 * time.Millisecond}
	if err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 probe call, got %d", got)
	}
}

func TestHTTPReady_SucceedsAfterRetries(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{
		{stdout: "503", exitCode: 22}, // curl exits non-zero for 5xx without -f
		{stdout: "503", exitCode: 22},
		{stdout: "200", exitCode: 0},
	}, &calls)
	g := HTTPReady{Port: 18008, Path: "/health", Timeout: 1 * time.Second, Poll: 10 * time.Millisecond}
	if err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe); err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 probe calls, got %d", got)
	}
}

func TestHTTPReady_TimesOut(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{{stdout: "503", exitCode: 22}}, &calls)
	g := HTTPReady{Port: 18008, Path: "/health", Timeout: 30 * time.Millisecond, Poll: 5 * time.Millisecond}
	err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "did not return 2xx") {
		t.Errorf("expected timeout message, got %v", err)
	}
}

func TestHTTPReady_SSHErrorTreatedAsRetryable(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{
		{err: errors.New("connection refused")},
		{stdout: "200", exitCode: 0},
	}, &calls)
	g := HTTPReady{Port: 18008, Path: "/health", Timeout: 1 * time.Second, Poll: 10 * time.Millisecond}
	if err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe); err != nil {
		t.Fatalf("expected eventual success after transient ssh error, got %v", err)
	}
}

func TestHTTPReady_CancelledContext(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{{stdout: "503", exitCode: 22}}, &calls)
	g := HTTPReady{Port: 18008, Path: "/health", Timeout: 5 * time.Second, Poll: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Wait(ctx, inventory.Host{Name: "h"}, probe); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSystemdActive_Succeeds(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{{stdout: "active\n", exitCode: 0}}, &calls)
	g := SystemdActive{Unit: "frameworks-foghorn", Poll: 10 * time.Millisecond}
	if err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestSystemdActive_RetriesUntilActive(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{
		{stdout: "activating\n", exitCode: 3},
		{stdout: "activating\n", exitCode: 3},
		{stdout: "active\n", exitCode: 0},
	}, &calls)
	g := SystemdActive{Unit: "frameworks-foghorn", Timeout: 1 * time.Second, Poll: 5 * time.Millisecond}
	if err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe); err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 probe calls, got %d", got)
	}
}

func TestSystemdActive_TimesOut(t *testing.T) {
	var calls atomic.Int32
	probe := scriptedProbe([]probeStep{{stdout: "failed\n", exitCode: 3}}, &calls)
	g := SystemdActive{Unit: "frameworks-foghorn", Timeout: 30 * time.Millisecond, Poll: 5 * time.Millisecond}
	err := g.Wait(context.Background(), inventory.Host{Name: "h"}, probe)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "did not reach 'active'") {
		t.Errorf("expected timeout message, got %v", err)
	}
}

func TestGate_NilProbeFunc(t *testing.T) {
	if err := (HTTPReady{Port: 1, Path: "/"}).Wait(context.Background(), inventory.Host{}, nil); err == nil {
		t.Error("HTTPReady should reject nil probe func")
	}
	if err := (SystemdActive{Unit: "x"}).Wait(context.Background(), inventory.Host{}, nil); err == nil {
		t.Error("SystemdActive should reject nil probe func")
	}
}

func TestGateForService(t *testing.T) {
	cases := []struct {
		svc       string
		wantType  string
		wantDescr string
	}{
		// HTTP-protocol services in servicedefs → HTTPReady
		{"foghorn", "HTTPReady", "HTTPReady{127.0.0.1:18008/health}"},
		{"bridge", "HTTPReady", "HTTPReady{127.0.0.1:18000/health}"},
		{"commodore", "HTTPReady", "HTTPReady{127.0.0.1:18001/health}"},
		// gRPC service → no HTTP health → SystemdActive
		{"decklog", "SystemdActive", "SystemdActive{frameworks-decklog}"},
		// TCP-protocol infra → SystemdActive
		{"postgres", "SystemdActive", "SystemdActive{frameworks-postgres}"},
		{"kafka", "SystemdActive", "SystemdActive{frameworks-kafka}"},
		// Unknown service → SystemdActive fallback
		{"this-service-does-not-exist", "SystemdActive", "SystemdActive{frameworks-this-service-does-not-exist}"},
	}
	for _, tc := range cases {
		t.Run(tc.svc, func(t *testing.T) {
			g := GateForService(tc.svc)
			switch tc.wantType {
			case "HTTPReady":
				if _, ok := g.(HTTPReady); !ok {
					t.Fatalf("GateForService(%q) = %T, want HTTPReady", tc.svc, g)
				}
			case "SystemdActive":
				if _, ok := g.(SystemdActive); !ok {
					t.Fatalf("GateForService(%q) = %T, want SystemdActive", tc.svc, g)
				}
			}
			if got := g.Describe(); got != tc.wantDescr {
				t.Errorf("Describe() = %q, want %q", got, tc.wantDescr)
			}
		})
	}
}
