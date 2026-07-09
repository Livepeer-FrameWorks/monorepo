package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectServiceControllerHonorsEnvOverride(t *testing.T) {
	cases := map[string]ServiceController{
		"s6":      s6Controller{},
		"systemd": systemdController{},
		"launchd": launchdController{},
		"S6":      s6Controller{},
	}
	for value, want := range cases {
		t.Setenv("HELMSMAN_SUPERVISOR", value)
		got := detectServiceController()
		if got != want {
			t.Fatalf("HELMSMAN_SUPERVISOR=%s: got %T, want %T", value, got, want)
		}
	}
}

func TestSetServiceControllerForTestRestores(t *testing.T) {
	original := currentServiceController()
	fake := fakeServiceController{}
	restore := SetServiceControllerForTest(fake)
	if currentServiceController() != fake {
		t.Fatal("override not applied")
	}
	restore()
	if currentServiceController() != original {
		t.Fatal("restore did not reinstate previous controller")
	}
}

type fakeServiceController struct{}

func (fakeServiceController) RestartCaddy(context.Context) error   { return nil }
func (fakeServiceController) SignalMistUSR1(context.Context) error { return nil }

func TestS6RestartCaddyStopsAndWaitsForAdmin(t *testing.T) {
	var stops, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/stop":
			stops.Add(1)
		case r.Method == http.MethodGet && r.URL.Path == "/config/":
			polls.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("CADDY_ADMIN_SOCKET", "")
	t.Setenv("CADDY_ADMIN_URL", server.URL)
	restoreTimings := setCaddyRestartTimingsForTest(t, 10*time.Millisecond, time.Second, 10*time.Millisecond)
	defer restoreTimings()

	if err := (s6Controller{}).RestartCaddy(context.Background()); err != nil {
		t.Fatalf("RestartCaddy: %v", err)
	}
	if stops.Load() != 1 {
		t.Fatalf("stops = %d, want 1", stops.Load())
	}
	if polls.Load() == 0 {
		t.Fatal("expected at least one readiness poll")
	}
}

func TestS6RestartCaddyFailsWhenAdminNeverReturns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	t.Setenv("CADDY_ADMIN_SOCKET", "")
	t.Setenv("CADDY_ADMIN_URL", url)
	restoreTimings := setCaddyRestartTimingsForTest(t, time.Millisecond, 100*time.Millisecond, 10*time.Millisecond)
	defer restoreTimings()

	if err := (s6Controller{}).RestartCaddy(context.Background()); err == nil {
		t.Fatal("expected error when admin endpoint never comes back")
	}
}

func setCaddyRestartTimingsForTest(t *testing.T, grace, timeout, poll time.Duration) func() {
	t.Helper()
	prevGrace, prevTimeout, prevPoll := caddyRestartGrace, caddyRestartTimeout, caddyReadyPollEvery
	caddyRestartGrace, caddyRestartTimeout, caddyReadyPollEvery = grace, timeout, poll
	return func() {
		caddyRestartGrace, caddyRestartTimeout, caddyReadyPollEvery = prevGrace, prevTimeout, prevPoll
	}
}
