package handlers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// Mist reports no health block for a stream that has no tracks yet (an input
// still booting or already draining, live or VOD). That is an expected state on
// every poll; only a stream that reports tracks without health is anomalous.
func TestStreamWithoutTracksAndHealthDoesNotWarn(t *testing.T) {
	previous := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = previous })
	logs := logrustest.NewLocal(monitorLogger)
	pm := &PrometheusMonitor{sendControlTrigger: func(*ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error) {
		return &control.MistTriggerResult{}, nil
	}}
	warnings := func() int {
		n := 0
		for _, entry := range logs.AllEntries() {
			if entry.Message == "No health data found for stream" && entry.Level <= logrus.WarnLevel {
				n++
			}
		}
		return n
	}
	now := time.Now()
	for _, stream := range []string{"vod+abc", "live+abc"} {
		pm.processObservedStreamDataContext(context.Background(), "node", stream,
			map[string]any{"inputs": float64(1), "tracks": float64(0)}, now.Add(-time.Second), now)
	}
	if got := warnings(); got != 0 {
		t.Fatalf("trackless streams without health warned %d times, want 0", got)
	}

	pm.processObservedStreamDataContext(context.Background(), "node", "live+abc",
		map[string]any{"inputs": float64(1), "tracks": float64(2)}, now.Add(-time.Second), now)
	if got := warnings(); got != 1 {
		t.Fatalf("stream with tracks but no health warned %d times, want 1", got)
	}
}
