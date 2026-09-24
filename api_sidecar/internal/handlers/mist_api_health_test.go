package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func pollActiveStreamsOnce(t *testing.T, pm *PrometheusMonitor, baseURL string) {
	t.Helper()
	client := mist.NewClient(logging.NewLogger(), mist.ClientConfig{BaseURL: baseURL, Username: "u", Password: "p"})
	var mu sync.Mutex
	pm.emitStreamLifecycleWithClient(context.Background(), "node-a", baseURL, client, &mu, func() bool { return true })
}

func lifecycleHealth(t *testing.T, pm *PrometheusMonitor) (healthy, reported, reachable bool) {
	t.Helper()
	nlu := pm.convertNodeAPIToMistTrigger("node-a", map[string]any{"cpu": float64(1)}, logging.NewLogger()).GetNodeLifecycleUpdate()
	return nlu.GetIsHealthy(), nlu.MistApiReachable != nil, nlu.GetMistApiReachable()
}

// A wedged controller (accepts nothing, like the staging edge after a rolling
// reload) must turn the node unhealthy after the threshold, even though the
// metrics payload alone looks fine, and recover once api2 answers again.
func TestMistAPIUnreachableMarksNodeUnhealthyAndRecovers(t *testing.T) {
	pm := newStreamWSTestMonitor(t)

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	if healthy, reported, reachable := lifecycleHealth(t, pm); !healthy || !reported || !reachable {
		t.Fatalf("fresh monitor: healthy=%v reported=%v reachable=%v, want all true", healthy, reported, reachable)
	}

	for i := 1; i < mistAPIUnreachableThreshold; i++ {
		pollActiveStreamsOnce(t, pm, deadURL)
		if healthy, _, reachable := lifecycleHealth(t, pm); !healthy || !reachable {
			t.Fatalf("after %d failure(s) node went unhealthy before the threshold", i)
		}
	}
	pollActiveStreamsOnce(t, pm, deadURL)
	healthy, reported, reachable := lifecycleHealth(t, pm)
	if healthy || !reported || reachable {
		t.Fatalf("after threshold: healthy=%v reported=%v reachable=%v, want unhealthy and explicitly unreachable", healthy, reported, reachable)
	}

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("command"), "authorize") {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"active_streams":{}}`))
	}))
	t.Cleanup(live.Close)
	pollActiveStreamsOnce(t, pm, live.URL)
	if healthy, _, reachable := lifecycleHealth(t, pm); !healthy || !reachable {
		t.Fatalf("after a successful api2 poll: healthy=%v reachable=%v, want recovered", healthy, reachable)
	}
}

// Authentication rejections count the same as transport failures: Helmsman
// cannot drive a controller it cannot log into.
func TestMistAPIAuthFailuresCountAsUnreachable(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(rejecting.Close)
	for i := 0; i < mistAPIUnreachableThreshold; i++ {
		pollActiveStreamsOnce(t, pm, rejecting.URL)
	}
	if healthy, _, reachable := lifecycleHealth(t, pm); healthy || reachable {
		t.Fatalf("auth failures: healthy=%v reachable=%v, want unhealthy", healthy, reachable)
	}
}
