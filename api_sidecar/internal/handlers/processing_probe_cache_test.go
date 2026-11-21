package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestCachedProbeAdmitsWithoutOpeningSource(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job, hash = "processing+cached-probe", "cached-probe", "cached-probe"
	req := &ipcpb.ProcessingJobRequest{JobId: job, ArtifactHash: hash, TenantId: "tenant", Params: map[string]string{}}
	probe := mist.ProcessingSourceProbe{SourceKey: processingProbeIdentity(req), Source: mist.SourceMediaInfo{VideoCodec: "H264", Width: 1920, Height: 1080}, Outputs: map[string]string{"width": "1920", "height": "1080", "duration_ms": "60000"}}
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	req.Params[mist.ProcessingSourceProbeParam] = string(encoded)
	if err = setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return 8 * 1024 * memoryMiB, 8 * 1024 * memoryMiB, nil }}
	var messages []*ipcpb.ControlMessage
	// A nil Mist client proves that cached admission makes no source/controller request.
	_, source, err := h.prepareProcessingMemory(discardLog(), nil, req, stream, "unused.mkv", `[{"process":"AV","codec":"H264","resolution":"0x720"}]`, nil, func(message *ipcpb.ControlMessage) { messages = append(messages, message) })
	if err != nil || source.Width != 1920 || source.Height != 1080 {
		t.Fatalf("cached source was re-probed or corrupted: %+v %v", source, err)
	}
	if len(messages) != 2 || messages[0].GetProcessingJobResult().GetOutputs()[mist.ProcessingSourceProbeParam] == "" || messages[1].GetProcessingJobResult().GetOutputs()[mist.ProcessingSourceProbeParam] == "" {
		t.Fatal("probe was not retained in both durable cache updates")
	}
}

func TestCachedProbeRejectsChangedSourceIdentity(t *testing.T) {
	old := mist.ProcessingSourceIdentity("tenant", "job", "asset", "old-source")
	probe := mist.ProcessingSourceProbe{SourceKey: old, Source: mist.SourceMediaInfo{Width: 1920, Height: 1080}, Outputs: map[string]string{}}
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	req := &ipcpb.ProcessingJobRequest{Params: map[string]string{mist.ProcessingSourceKeyParam: mist.ProcessingSourceIdentity("tenant", "job", "asset", "new-source"), mist.ProcessingSourceProbeParam: string(encoded)}}
	if hasCachedProcessingProbe(req) {
		t.Fatal("new source reused an obsolete probe")
	}
}
