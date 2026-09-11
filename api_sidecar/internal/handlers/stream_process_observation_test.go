package handlers

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestStreamProcessObservationPreservesRawIdentities(t *testing.T) {
	data := map[string]any{"pid": float64(90), "sourcepids": []any{float64(23), float64(21), float64(23)}, "firstms": float64(0), "lastms": float64(8123), "inputs": float64(1)}
	trigger := convertStreamAPIToMistTrigger("node", "live+stream", "stream", data, nil, nil, 0, logging.NewLogger())
	lifecycle := trigger.GetStreamLifecycleUpdate()
	observation := lifecycle.GetProcessObservation()
	if observation.GetRuntimeName() != "live+stream" || observation.GetBufferPid() != 90 || !observation.GetSourcePidsKnown() || !reflect.DeepEqual(observation.GetSourcePids(), []int64{21, 23}) {
		t.Fatalf("lost process identity: %v", observation)
	}
	if observation.FirstMediaMs == nil || observation.GetFirstMediaMs() != 0 || observation.GetLastMediaMs() != 8123 {
		t.Fatalf("lost media timestamps: %v", observation)
	}
	if observation.GetReadStartedUnixMillis() != 0 || observation.GetReadCompletedUnixMillis() != 0 || lifecycle.GetIngestGeneration() != "" || lifecycle.GetIngestConnectorPid() != 0 {
		t.Fatal("converter invented a read window or admitted runtime binding")
	}
}

func TestStreamProcessObservationUnknownIsNotEmpty(t *testing.T) {
	for _, value := range []any{nil, "[]", []any{float64(1), "2"}, []any{float64(-1)}, []any{float64(0)}, []any{1.5}, []any{math.NaN()}, []any{math.Inf(1)}, []any{float64(1 << 53)}} {
		observation := streamProcessObservation("live+stream", map[string]any{"sourcepids": value})
		if observation.GetSourcePidsKnown() || len(observation.GetSourcePids()) > 0 {
			t.Fatalf("invalid census became authoritative: %#v", value)
		}
	}
	if observation := streamProcessObservation("live+stream", map[string]any{"sourcepids": []any{}}); !observation.GetSourcePidsKnown() || len(observation.GetSourcePids()) != 0 {
		t.Fatalf("empty census lost authority: %v", observation)
	}
	if observation := streamProcessObservation("live+stream", nil); observation.GetSourcePidsKnown() {
		t.Fatal("missing census became authoritative")
	}
}

func TestStreamProcessObservationRejectsRoundedNumbers(t *testing.T) {
	for _, value := range []any{nil, "12", float64(-1), 1.5, math.NaN(), math.Inf(1), float64(1 << 53)} {
		observation := streamProcessObservation("live+stream", map[string]any{"pid": value, "firstms": value, "lastms": value})
		if observation.BufferPid != nil || observation.FirstMediaMs != nil || observation.LastMediaMs != nil {
			t.Fatalf("invalid number retained: %#v: %v", value, observation)
		}
	}
}

func TestObservedStreamDataCarriesReadWindowWithoutGeneration(t *testing.T) {
	previousLogger := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = previousLogger })
	var sent *ipcpb.MistTrigger
	pm := &PrometheusMonitor{sendControlTrigger: func(trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		sent = trigger
		return &control.MistTriggerResult{}, nil
	}}
	started := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	completed := started.Add(time.Second)
	pm.processObservedStreamDataContext(context.Background(), "node", "live+stream", map[string]any{"pid": float64(90), "sourcepids": []any{float64(21)}, "inputs": float64(1)}, started, completed)
	observation := sent.GetStreamLifecycleUpdate().GetProcessObservation()
	if observation.GetReadStartedUnixMillis() != started.UnixMilli() || observation.GetReadCompletedUnixMillis() != completed.UnixMilli() {
		t.Fatalf("read window was lost or replaced by send time: %v", observation)
	}
	if sent.GetStreamLifecycleUpdate().GetIngestGeneration() != "" || sent.GetStreamLifecycleUpdate().GetIngestConnectorPid() != 0 {
		t.Fatal("raw census became an admission binding")
	}
	pm.processObservedStreamDataContext(context.Background(), "node", "live+stream", nil, completed, started)
	if observation = sent.GetStreamLifecycleUpdate().GetProcessObservation(); observation.GetReadStartedUnixMillis() != 0 || observation.GetReadCompletedUnixMillis() != 0 {
		t.Fatal("backwards read window was retained")
	}
}
