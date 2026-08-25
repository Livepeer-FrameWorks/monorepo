package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestBeaconMetricsClassifyTokenAttribution(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	intake := NewBeaconIntake(nil, nil, secret, logging.NewLogger())
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_player_telemetry_events_total"}, []string{"type", "outcome"})
	intake.SetMetrics(&BeaconMetrics{Events: events})

	if _, ok, outcome := intake.clusterClaims("content-1", ""); ok || outcome != "accepted_unattributed" {
		t.Fatal("empty token unexpectedly attributed")
	}
	if _, ok, outcome := intake.clusterClaims("content-1", "bad-token"); ok || outcome != "invalid_token" {
		t.Fatal("invalid token unexpectedly attributed")
	}
	token, err := telemetrytoken.Sign(secret, telemetrytoken.Claims{
		ContentID: "content-1", NodeID: "node-1", ServingClusterID: "cluster-1",
	}, time.Minute, time.Now())
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	if _, ok, outcome := intake.clusterClaims("content-1", token); !ok || outcome != "accepted_attributed" {
		t.Fatal("valid token was not attributed")
	}

	for _, outcome := range []string{"accepted_unattributed", "invalid_token", "accepted_attributed"} {
		if got := metricValue(t, events, "boot", outcome); got != 0 {
			t.Fatalf("classification emitted terminal metric %s = %v", outcome, got)
		}
	}
}

type failingBeaconSink struct{}

func (failingBeaconSink) SendTriggerContext(context.Context, *ipcpb.MistTrigger) error {
	return errors.New("decklog unavailable")
}

func TestBeaconMetricsRecordOneTerminalOutcome(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	resolver := &fakeResolver{stream: &commodorepb.ResolvePlaybackIDResponse{TenantId: "11111111-1111-1111-1111-111111111111"}}
	intake := NewBeaconIntake(resolver, fakeLimiter{allow: true}, secret, logging.NewLogger())
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_terminal_player_telemetry_events_total"}, []string{"type", "outcome"})
	intake.SetMetrics(&BeaconMetrics{Events: events})
	token, err := telemetrytoken.Sign(secret, telemetrytoken.Claims{ContentID: "content-1", NodeID: "node-1", ServingClusterID: "cluster-1"}, time.Minute, time.Now())
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	handler := NewPlaybackTelemetryHandler(intake, failingBeaconSink{})
	postBoot(handler, `{"contentId":"content-1","telemetryToken":"`+token+`"}`)

	for outcome, want := range map[string]float64{
		"accepted_attributed": 0,
		"decklog_error":       1,
	} {
		if got := metricValue(t, events, "boot", outcome); got != want {
			t.Fatalf("%s = %v, want %v", outcome, got, want)
		}
	}
}

func metricValue(t *testing.T, events *prometheus.CounterVec, beaconType, outcome string) float64 {
	t.Helper()
	var metric dto.Metric
	if err := events.WithLabelValues(beaconType, outcome).Write(&metric); err != nil {
		t.Fatalf("write %s metric: %v", outcome, err)
	}
	return metric.GetCounter().GetValue()
}
