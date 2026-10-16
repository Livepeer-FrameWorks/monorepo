package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// A single failed sentinel read inside storeReadinessWindow does not change
// readiness, so it is counted but not logged. The warning fires once, when the
// store stays unreadable past the window, and the recovery is logged only after
// such a warning.
func TestStoreProbeWarnsOnlyWhenReadinessWindowFails(t *testing.T) {
	h, _, _, _ := newTestHandler(&fakeS3{data: []byte("ready")}, "")
	logs := logrustest.NewLocal(h.logger)
	probes := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_store_probes_total"}, []string{"result"})
	h.SetStoreProbeCounter(probes)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	stall := errors.New("get readiness sentinel: context deadline exceeded")
	warnings := func() int {
		n := 0
		for _, e := range logs.AllEntries() {
			if e.Level <= logrus.WarnLevel {
				n++
			}
		}
		return n
	}

	h.recordStoreProbe(nil)
	now = now.Add(storeProbeInterval)
	h.recordStoreProbe(stall)
	now = now.Add(storeProbeInterval)
	h.recordStoreProbe(nil)
	if got := warnings(); got != 0 {
		t.Fatalf("a failed read inside the readiness window logged %d warnings, want 0", got)
	}
	if n := len(logs.AllEntries()); n != 0 {
		t.Fatalf("a blip inside the window logged %d entries, last %q", n, logs.LastEntry().Message)
	}

	for range 6 {
		now = now.Add(storeProbeInterval)
		h.recordStoreProbe(stall)
	}
	if got := warnings(); got != 1 {
		t.Fatalf("a store unreadable past the window logged %d warnings, want 1", got)
	}
	h.recordStoreProbe(nil)
	if last := logs.LastEntry(); last == nil || last.Level != logrus.InfoLevel {
		t.Fatal("recovery after a window failure must be logged")
	}

	if got := testutil.ToFloat64(probes.WithLabelValues("unavailable")); got != 7 {
		t.Fatalf("unavailable probes counted %v, want 7", got)
	}
	if got := testutil.ToFloat64(probes.WithLabelValues("ok")); got != 3 {
		t.Fatalf("ok probes counted %v, want 3", got)
	}
}
