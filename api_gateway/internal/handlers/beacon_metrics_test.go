package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestBeaconMetricsClassifyTokenAttribution(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	intake := NewBeaconIntake(nil, nil, secret, logging.NewLogger())
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_player_telemetry_events_total"}, []string{"type", "outcome"})
	intake.SetMetrics(&BeaconMetrics{Events: events})

	if _, ok := intake.clusterClaims("boot", "content-1", ""); ok {
		t.Fatal("empty token unexpectedly attributed")
	}
	if _, ok := intake.clusterClaims("boot", "content-1", "bad-token"); ok {
		t.Fatal("invalid token unexpectedly attributed")
	}
	token, err := telemetrytoken.Sign(secret, telemetrytoken.Claims{
		ContentID: "content-1", NodeID: "node-1", ServingClusterID: "cluster-1",
	}, time.Minute, time.Now())
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	if _, ok := intake.clusterClaims("boot", "content-1", token); !ok {
		t.Fatal("valid token was not attributed")
	}

	for outcome, want := range map[string]float64{
		"accepted_unattributed": 1,
		"invalid_token":         1,
		"accepted_attributed":   1,
	} {
		var metric dto.Metric
		if err := events.WithLabelValues("boot", outcome).Write(&metric); err != nil {
			t.Fatalf("write %s metric: %v", outcome, err)
		}
		if got := metric.GetCounter().GetValue(); got != want {
			t.Fatalf("%s = %v, want %v", outcome, got, want)
		}
	}
}
