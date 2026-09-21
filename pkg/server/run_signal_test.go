package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// readyRouter serves /ready from the readiness checker, as NewServiceRouter does.
func readyRouter(ready *monitoring.ReadinessChecker) http.Handler {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/ready", ready.Handler())
	return r
}

func getStatus(url string) (int, error) {
	client := http.Client{Timeout: time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func waitServing(t *testing.T, url string) {
	t.Helper()
	for range 100 {
		if _, err := getStatus(url); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never served", url)
}

func withPreStopDelay(t *testing.T, d time.Duration) {
	t.Helper()
	previous := preStopDelay
	preStopDelay = d
	t.Cleanup(func() { preStopDelay = previous })
}

// SIGTERM is a planned stop: the signal hook receives it without owning a
// signal channel of its own.
func TestRunPassesStopSignalToSignalStopHooks(t *testing.T) {
	resetReloadFnsForTest(t)
	withPreStopDelay(t, 0)
	lis := listenLocal(t)
	ready := monitoring.NewReadinessChecker("svc-signal", "v1")
	got := make(chan os.Signal, 1)
	var drainedAfterSignal bool
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(context.Background(), RunSpec{
			Service: "svc-signal",
			Logger:  logging.NewLogger(),
			Ready:   ready,
			HTTP:    []HTTPListener{{Name: "http", Listener: lis, Handler: readyRouter(ready)}},
			OnSignalStop: []func(context.Context, os.Signal){func(_ context.Context, sig os.Signal) {
				got <- sig
			}},
			OnDrain: []func(context.Context){func(context.Context) { drainedAfterSignal = len(got) == 1 }},
		})
	}()
	waitServing(t, "http://"+lis.Addr().String()+"/ready")

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on SIGTERM")
	}
	select {
	case sig := <-got:
		if sig != syscall.SIGTERM {
			t.Fatalf("hook got %v, want SIGTERM", sig)
		}
	default:
		t.Fatal("OnSignalStop hook did not run on SIGTERM")
	}
	if !drainedAfterSignal {
		t.Fatal("OnSignalStop must run before OnDrain")
	}
}

// A listener failure is not a planned stop, so signal hooks do not run.
func TestRunSkipsSignalStopHooksOnListenerFailure(t *testing.T) {
	resetReloadFnsForTest(t)
	withPreStopDelay(t, time.Minute)
	called := false
	start := time.Now()
	err := Run(context.Background(), RunSpec{
		Service: "chandler",
		Logger:  logging.NewLogger(),
		GRPC: []GRPCListener{{Name: "grpc", Listener: listenLocal(t), Build: func(context.Context) (*grpc.Server, error) {
			return nil, errors.New("tls files missing")
		}}},
		OnSignalStop: []func(context.Context, os.Signal){func(context.Context, os.Signal) { called = true }},
	})
	if err == nil {
		t.Fatal("Run must report the build failure")
	}
	if called {
		t.Fatal("OnSignalStop ran for a listener failure")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("a failure stop waited the pre-stop delay (%s)", elapsed)
	}
}

// After a stop signal, a service whose readiness pollers probe /ready keeps
// serving the draining 503 for the pre-stop delay before its listeners close.
func TestRunServesDrainingReadinessDuringPreStopDelay(t *testing.T) {
	resetReloadFnsForTest(t)
	const delay = 600 * time.Millisecond
	withPreStopDelay(t, delay)
	if !servicedefs.Services["chandler"].ReadinessPolled() {
		t.Fatal("fixture needs a service whose readiness is polled on /ready")
	}
	lis := listenLocal(t)
	ready := monitoring.NewReadinessChecker("chandler", "v1")
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(context.Background(), RunSpec{
			Service: "chandler",
			Logger:  logging.NewLogger(),
			Ready:   ready,
			HTTP:    []HTTPListener{{Name: "http", Listener: lis, Handler: readyRouter(ready)}},
		})
	}()
	url := "http://" + lis.Addr().String() + "/ready"
	waitServing(t, url)

	signalled := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	time.Sleep(delay / 3)
	status, err := getStatus(url)
	if err != nil || status != http.StatusServiceUnavailable {
		t.Fatalf("GET /ready during the pre-stop delay = %d, %v; want a served 503", status, err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if elapsed := time.Since(signalled); elapsed < delay {
		t.Fatalf("Run stopped %s after the signal, before the %s pre-stop delay", elapsed, delay)
	}
	var d net.Dialer
	if conn, err := d.DialContext(context.Background(), "tcp", lis.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("listener still accepts connections after Run returned")
	}
}

// stopBudget reads the duration after key (for example "TimeoutStopSec=120s").
func stopBudget(content, key string) (time.Duration, bool) {
	_, rest, ok := strings.Cut(content, key)
	if !ok {
		return 0, false
	}
	value, _, _ := strings.Cut(rest, "\n")
	d, err := time.ParseDuration(strings.TrimSpace(value))
	return d, err == nil
}

// The delay covers the slowest readiness poller and fits in the stop budget
// systemd and Compose give a service, with the shutdown timeout after it.
func TestPreStopDelayCoversPollersAndFitsStopBudget(t *testing.T) {
	// Quartermaster polls every 30 s with up to 25 % jitter; Navigator's
	// Cloudflare monitors probe every 60 s with a 5 s timeout.
	if ReadinessPropagationDelay < 60*time.Second+5*time.Second || ReadinessPropagationDelay < 30*time.Second*5/4 {
		t.Fatalf("ReadinessPropagationDelay = %s does not cover one poller interval plus probe timeout", ReadinessPropagationDelay)
	}
	budget := ReadinessPropagationDelay + defaultShutdownTimeout
	for path, want := range map[string]string{
		"../../ansible/collections/ansible_collections/frameworks/infra/roles/go_service/templates/service.j2":                "TimeoutStopSec=",
		"../../ansible/collections/ansible_collections/frameworks/infra/roles/compose_stack/templates/generic-compose.yml.j2": "stop_grace_period: ",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		limit, ok := stopBudget(string(content), want)
		if !ok {
			t.Fatalf("%s sets no %s; the platform default cannot hold the pre-stop delay", path, want)
		}
		if limit <= budget {
			t.Fatalf("%s %s%s <= pre-stop delay + shutdown timeout %s", path, want, limit, budget)
		}
	}
}
