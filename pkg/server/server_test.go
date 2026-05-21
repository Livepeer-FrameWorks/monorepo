package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/gin-gonic/gin"
)

// resetReloadFnsForTest restores the package-level callback list to empty.
// Tests register callbacks via RegisterReload and must not leak state into
// neighbouring tests.
func resetReloadFnsForTest(t *testing.T) {
	t.Helper()
	reloadMu.Lock()
	reloadFns = nil
	reloadMu.Unlock()
}

func TestSetupServiceRouter(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc-setup", "v1")
	mc := monitoring.NewMetricsCollector("svc-setup", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ping", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSetupServiceRouterHandlesAlternateTrailingSlash(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc-slash", "v1")
	mc := monitoring.NewMetricsCollector("svc-slash", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.POST("/api/action", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "/api/action/", nil)
	req.Header.Set("Origin", "https://app.frameworks.network")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected alternate trailing slash to dispatch, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.frameworks.network" {
		t.Fatalf("expected CORS header on slash mismatch, got %q", got)
	}
}

// TestRegisterReload_AppendOrder verifies callbacks accumulate in
// registration order and that nil registrations are ignored.
func TestRegisterReload_AppendOrder(t *testing.T) {
	resetReloadFnsForTest(t)
	t.Cleanup(func() { resetReloadFnsForTest(t) })

	var seen []int
	RegisterReload(func() error { seen = append(seen, 1); return nil })
	RegisterReload(nil) // must be ignored
	RegisterReload(func() error { seen = append(seen, 2); return nil })
	RegisterReload(func() error { seen = append(seen, 3); return nil })

	for _, fn := range snapshotReloadFns() {
		_ = fn()
	}
	if got, want := seen, []int{1, 2, 3}; len(got) != len(want) || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("callback order: want %v, got %v", want, got)
	}
}

// TestSnapshotReloadFns_IsCopy verifies that mutating a snapshot does not
// poison the package-level list — callers can safely iterate without
// holding the mutex.
func TestSnapshotReloadFns_IsCopy(t *testing.T) {
	resetReloadFnsForTest(t)
	t.Cleanup(func() { resetReloadFnsForTest(t) })

	RegisterReload(func() error { return nil })
	snap := snapshotReloadFns()
	snap[0] = nil

	again := snapshotReloadFns()
	if len(again) != 1 || again[0] == nil {
		t.Fatalf("snapshot mutation leaked back into reloadFns")
	}
}

// TestStartReloadListener_FiresCallbacks proves the end-to-end SIGHUP →
// dispatcher → callback wiring. The whole point of L3a: the process keeps
// running when the signal arrives, and every registered callback runs in
// order. Catches regressions where the listener stops consuming SIGHUP
// (which would let Go's default-terminate disposition kill the process).
func TestStartReloadListener_FiresCallbacks(t *testing.T) {
	resetReloadFnsForTest(t)
	t.Cleanup(func() { resetReloadFnsForTest(t) })

	var ran atomic.Int32
	fired := make(chan struct{}, 1)
	RegisterReload(func() error {
		ran.Add(1)
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	})

	stop := startReloadListener(logging.NewLogger(), "test")
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatalf("callback did not fire within 2s; ran=%d", ran.Load())
	}
	if got := ran.Load(); got != 1 {
		t.Errorf("expected exactly 1 callback run, got %d", got)
	}
}

// TestStartReloadListener_NoCallbackDoesNotTerminate is the headline L3a
// property: if Start installs the SIGHUP listener but nothing calls
// RegisterReload, the process must still survive a SIGHUP. Go's default
// disposition for SIGHUP is to terminate; signal.Notify in the listener
// is what neuters that. If this test starts being skipped or removed, a
// universal `ExecReload=` (any future regression) silently breaks every
// service that hasn't wired a callback.
func TestStartReloadListener_NoCallbackDoesNotTerminate(t *testing.T) {
	resetReloadFnsForTest(t)
	t.Cleanup(func() { resetReloadFnsForTest(t) })

	stop := startReloadListener(logging.NewLogger(), "test")
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	// Give the listener a moment to consume the signal. If the process
	// is going to die from SIGHUP, the test runner registers the death
	// here; if we reach the assertion below, the listener absorbed it.
	time.Sleep(150 * time.Millisecond)

	if got := len(snapshotReloadFns()); got != 0 {
		t.Errorf("expected zero callbacks registered, got %d", got)
	}
}

// TestStartReloadListener_CallbackErrorDoesNotAbortSubsequent verifies
// that a returning-error callback is logged but the rest still fire on
// the same signal. Important so one buggy reload (say, a malformed
// config file) doesn't strand later callbacks that could rotate certs.
func TestStartReloadListener_CallbackErrorDoesNotAbortSubsequent(t *testing.T) {
	resetReloadFnsForTest(t)
	t.Cleanup(func() { resetReloadFnsForTest(t) })

	var firstRan, secondRan atomic.Bool
	done := make(chan struct{}, 1)
	RegisterReload(func() error {
		firstRan.Store(true)
		return errors.New("intentional reload failure")
	})
	RegisterReload(func() error {
		secondRan.Store(true)
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})

	stop := startReloadListener(logging.NewLogger(), "test")
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("second callback did not fire after first errored")
	}
	if !firstRan.Load() {
		t.Error("first callback did not run")
	}
	if !secondRan.Load() {
		t.Error("second callback did not run after first returned error")
	}
}

func TestSetupServiceRouterAlternateTrailingSlashDefaultsToOK(t *testing.T) {
	logger := logging.NewLogger()
	hc := monitoring.NewHealthChecker("svc-slash-default", "v1")
	mc := monitoring.NewMetricsCollector("svc-slash-default", "v1", "abc")
	r := SetupServiceRouter(logger, "svc", hc, mc)
	r.POST("/graphql", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte(`{"data":{"__typename":"Query"}}`))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "/graphql/", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected fallback handler to default to 200, got %d", w.Code)
	}
}
