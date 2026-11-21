package control

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"testing"
)

func TestDeferredVODReplacementMetricDoesNotClaimNoReplacement(t *testing.T) {
	previous := controlMetrics
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_stream_transcode_degraded_total", Help: "test"}, []string{"failed_process_type", "stream_kind", "replaced"})
	controlMetrics = &ControlMetrics{StreamTranscodeDegraded: counter}
	t.Cleanup(func() { controlMetrics = previous })
	processStreamTranscodeDegraded(&ipcpb.StreamTranscodeDegraded{Stream: "processing+deferred", FailedProcessType: "Livepeer", ReplacementCount: 0}, "edge", logging.NewLogger())
	if got := testutil.ToFloat64(counter.WithLabelValues("Livepeer", "processing", "deferred")); got != 1 {
		t.Fatalf("deferred VOD fallback labelled as no replacement: deferred counter=%v want 1", got)
	}
	if got := testutil.ToFloat64(counter.WithLabelValues("Livepeer", "processing", "false")); got != 0 {
		t.Fatalf("deferred VOD fallback increased no-replacement counter: %v", got)
	}
}
