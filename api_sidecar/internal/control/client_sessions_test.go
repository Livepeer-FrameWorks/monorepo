package control

import (
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// collectNodeFingerprint reads host network/identity facts. Exact values are
// environment-dependent, so the test pins the invariants the consumer relies
// on: loopback/link-local addresses are excluded, and any digest field is a
// well-formed SHA-256 hex string.
func TestCollectNodeFingerprint(t *testing.T) {
	fp := collectNodeFingerprint()
	if fp == nil {
		t.Fatal("fingerprint must not be nil")
	}

	for _, addr := range append(append([]string{}, fp.GetLocalIpv4()...), fp.GetLocalIpv6()...) {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("fingerprint contains unparseable IP %q", addr)
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Fatalf("fingerprint must exclude loopback/link-local, got %q", addr)
		}
	}
	for _, h := range []string{fp.GetMacsSha256(), fp.GetMachineIdSha256()} {
		if h == "" {
			continue
		}
		if len(h) != 64 {
			t.Fatalf("digest %q is not 32-byte hex", h)
		}
		if _, err := hex.DecodeString(h); err != nil {
			t.Fatalf("digest %q is not valid hex: %v", h, err)
		}
	}
}

// errMistServer answers every request with 500 so the Mist client's API call
// (and thus the session command) fails — exercises the handler error branch.
func errMistServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func withConfig(t *testing.T, cfg *sidecarcfg.HelmsmanConfig) {
	t.Helper()
	prev := currentConfig
	currentConfig = cfg
	t.Cleanup(func() { currentConfig = prev })
}

func TestHandleStopSessions(t *testing.T) {
	t.Run("empty stream list is a no-op", func(t *testing.T) {
		mock := newMockMistServer(t)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})
		handleStopSessions(logging.NewLogger(), &ipcpb.StopSessionsRequest{TenantId: "t1"})
		if n := len(mock.callsContainingKey("stop_sessions")); n != 0 {
			t.Fatalf("empty request must not call Mist; got %d calls", n)
		}
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		withConfig(t, nil)
		// Must not panic with no config and no Mist client.
		handleStopSessions(logging.NewLogger(), &ipcpb.StopSessionsRequest{
			TenantId:    "t1",
			StreamNames: []string{"live+a"},
		})
	})

	t.Run("forwards stop_sessions to Mist", func(t *testing.T) {
		mock := newMockMistServer(t)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})
		handleStopSessions(logging.NewLogger(), &ipcpb.StopSessionsRequest{
			TenantId:    "t1",
			Reason:      "suspended",
			StreamNames: []string{"live+a", "live+b"},
		})
		calls := mock.callsContainingKey("stop_sessions")
		if len(calls) != 1 {
			t.Fatalf("expected one stop_sessions call, got %d", len(calls))
		}
		if names, ok := calls[0]["stop_sessions"].([]any); !ok || len(names) != 2 {
			t.Fatalf("stop_sessions payload not forwarded: %v", calls[0]["stop_sessions"])
		}
	})

	t.Run("Mist API error is handled, not panicked", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		handleStopSessions(logging.NewLogger(), &ipcpb.StopSessionsRequest{
			TenantId:    "t1",
			StreamNames: []string{"live+a"},
		})
	})
}

func TestHandleInvalidateSessions(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		handleInvalidateSessions(logging.NewLogger(), nil)
	})

	t.Run("empty stream list is a no-op", func(t *testing.T) {
		mock := newMockMistServer(t)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})
		handleInvalidateSessions(logging.NewLogger(), &ipcpb.InvalidateSessionsRequest{TenantId: "t1"})
		if n := len(mock.callsContainingKey("invalidate_sessions")); n != 0 {
			t.Fatalf("empty request must not call Mist; got %d calls", n)
		}
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		withConfig(t, nil)
		handleInvalidateSessions(logging.NewLogger(), &ipcpb.InvalidateSessionsRequest{
			TenantId:    "t1",
			StreamNames: []string{"live+a"},
		})
	})

	t.Run("forwards invalidate_sessions to Mist", func(t *testing.T) {
		mock := newMockMistServer(t)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})
		handleInvalidateSessions(logging.NewLogger(), &ipcpb.InvalidateSessionsRequest{
			TenantId:    "t1",
			Reason:      "policy_change",
			StreamNames: []string{"live+a"},
		})
		calls := mock.callsContainingKey("invalidate_sessions")
		if len(calls) != 1 {
			t.Fatalf("expected one invalidate_sessions call, got %d", len(calls))
		}
	})

	t.Run("Mist API error is handled, not panicked", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		handleInvalidateSessions(logging.NewLogger(), &ipcpb.InvalidateSessionsRequest{
			TenantId:    "t1",
			StreamNames: []string{"live+a"},
		})
	})
}
