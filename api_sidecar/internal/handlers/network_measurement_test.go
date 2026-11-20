package handlers

import (
	"errors"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/netstat"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type networkFixtureReader struct {
	snapshot netstat.Snapshot
	err      error
}

func (r *networkFixtureReader) Read() (netstat.Snapshot, error) { return r.snapshot, r.err }

func TestNodeMeasurementPrefersNICAndMarksMistFallback(t *testing.T) {
	start := time.Now().Add(-20 * time.Second)
	reader := &networkFixtureReader{snapshot: netstat.Snapshot{At: start, Interfaces: []netstat.Counters{{Name: "eth0", Index: 7}}}}
	pm := &PrometheusMonitor{networkReader: reader, lastBwUp: 1000, lastBwDown: 5000, lastPollTime: time.Now().Add(-10 * time.Second)}
	pm.observeNetworkCounters()
	reader.snapshot = netstat.Snapshot{At: start.Add(10 * time.Second), Interfaces: []netstat.Counters{{Name: "eth0", Index: 7, Tx: 1250000000, Rx: 800000000}}}
	pm.observeNetworkCounters()
	data := map[string]any{"bw": []any{float64(1000), float64(5000)}}
	report := pm.convertNodeAPIToMistTrigger("edge", data, logging.NewLogger()).GetNodeLifecycleUpdate()
	if report.GetUpSpeed() != 125000000 || report.GetDownSpeed() != 80000000 || report.GetObservedSource() != ipcpb.ObservedSource_OBSERVED_SOURCE_NIC || !report.GetObservedValid() {
		t.Fatalf("NIC observation=%v", report)
	}
	reader.err = errors.New("NIC read failed")
	pm.observeNetworkCounters()
	pm.lastPollTime = time.Now().Add(-10 * time.Second)
	data["bw"] = []any{float64(100001000), float64(5000)}
	report = pm.convertNodeAPIToMistTrigger("edge", data, logging.NewLogger()).GetNodeLifecycleUpdate()
	if report.GetUpSpeed() < 9000000 || report.GetUpSpeed() > 10000000 || report.GetObservedSource() != ipcpb.ObservedSource_OBSERVED_SOURCE_MIST || !report.GetObservedValid() {
		t.Fatalf("marked Mist fallback=%v", report)
	}
}
