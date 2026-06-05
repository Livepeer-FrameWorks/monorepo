package resolvers

import (
	"context"
	"testing"
	"time"

	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestTenantMismatch(t *testing.T) {
	tenant := "tenant-1"
	otherTenant := "tenant-2"

	tests := []struct {
		name     string
		tenantID string
		event    *signalmanpb.SignalmanEvent
		want     bool
	}{
		{
			name:     "empty tenant skips mismatch",
			tenantID: "",
			event:    &signalmanpb.SignalmanEvent{TenantId: &tenant},
			want:     false,
		},
		{
			name:     "missing event tenant allowed (infra/system broadcasts)",
			tenantID: tenant,
			event:    &signalmanpb.SignalmanEvent{},
			want:     false,
		},
		{
			name:     "tenant match passes",
			tenantID: tenant,
			event:    &signalmanpb.SignalmanEvent{TenantId: &tenant},
			want:     false,
		},
		{
			name:     "tenant mismatch blocks",
			tenantID: tenant,
			event:    &signalmanpb.SignalmanEvent{TenantId: &otherTenant},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tenantMismatch(tt.tenantID, tt.event); got != tt.want {
				t.Fatalf("tenantMismatch = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunSignalmanSubscriptionTracksActiveGaugeUntilExit(t *testing.T) {
	metrics := &GraphQLMetrics{
		SubscriptionsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "test_subscription_active_count",
			Help: "Active test subscriptions",
		}, []string{"operation"}),
	}
	sm := NewSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{
		Metrics: metrics,
	})
	defer func() {
		if err := sm.Shutdown(); err != nil {
			t.Fatalf("shutdown subscription manager: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go sm.runSignalmanSubscription(
		ctx,
		"streams",
		ConnectionConfig{TenantID: "tenant-1"},
		nil,
		nil,
		func() { close(done) },
		func(*signalmanclient.GRPCClient) error { return nil },
		func(*signalmanpb.SignalmanEvent) bool { return true },
	)

	waitForGauge(t, metrics.SubscriptionsActive.WithLabelValues("streams"), 1)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscription goroutine did not exit after context cancel")
	}
	waitForGauge(t, metrics.SubscriptionsActive.WithLabelValues("streams"), 0)
}

func waitForGauge(t *testing.T, metric prometheus.Gauge, want float64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if got := gaugeValue(t, metric); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gauge = %v, want %v", gaugeValue(t, metric), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func gaugeValue(t *testing.T, metric prometheus.Gauge) float64 {
	t.Helper()
	dtoMetric := &dto.Metric{}
	if err := metric.Write(dtoMetric); err != nil {
		t.Fatalf("write gauge metric: %v", err)
	}
	return dtoMetric.GetGauge().GetValue()
}
