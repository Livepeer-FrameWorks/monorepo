package handlers

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

func TestLocalFallbackNeedsItsOwnMemoryAdmission(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+small-livepeer-fallback", "fallback-job"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(1536) * memoryMiB
	h := &ProcessingJobHandler{memoryControls: map[string]bool{"AV": true, "Thumbs": true}, memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	req := &ipcpb.ProcessingJobRequest{JobId: job}
	source := mist.SourceMediaInfo{Width: 3840, Height: 2160}
	policy := `[{"process":"Livepeer","target_profiles":[{"height":2160,"bitrate":16000000},{"height":1440,"bitrate":8000000},{"height":1080,"bitrate":5000000},{"height":720,"bitrate":3000000},{"height":480,"bitrate":1200000},{"height":360,"bitrate":800000}]}]`
	send := func(*ipcpb.ControlMessage) {}
	admitted, err := h.admitProcessingPolicy(discardLog(), req, stream, policy, source, send)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.admitProcessingPolicy(discardLog(), req, stream, mist.ReplaceLivepeerWithLocal(admitted), source, send)
	var capacity *processingCapacityError
	if !errors.As(err, &capacity) || !capacity.nodeTooSmall {
		t.Fatalf("oversized fallback did not refuse: %v", err)
	}
	out := processingMemoryFailureOutputs(err)
	if out[mist.ProcessingResultRetryCause] != mist.ProcessingRetryCauseMemoryNodeTooSmall || out[mist.ProcessingRequiredMemoryBytes] == "" || mist.HasLivepeerProcesses(out[mist.ProcessingMemoryPolicy]) {
		t.Fatalf("unplaceable fallback lost its routing contract: %v", out)
	}
	if got, _ := getProcessingProcessOverride(stream); got != admitted {
		t.Fatalf("unreserved local policy published: %s", got)
	}
}

func TestZeroWorkAdmissionKeepsGlobalProcessesInhibited(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+no-extra-processes", "no-extra-job"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(3072) * memoryMiB
	h := &ProcessingJobHandler{memoryControls: map[string]bool{"AV": true, "Thumbs": true}, memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	policy, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, "[]", mist.SourceMediaInfo{Width: 320, Height: 180}, func(*ipcpb.ControlMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(policy), &processes); err != nil || len(processes) == 0 || processes[0]["track_select"] != "video=none&audio=none&subtitle=none&meta=none" {
		t.Fatalf("empty override enables Mist's wildcard processes: %s (%v)", policy, err)
	}
	if bytes, err := processingMemoryEstimate(policy, mist.SourceMediaInfo{Width: 320, Height: 180}); err != nil || bytes != 0 {
		t.Fatalf("inhibited process consumed encoder reservation: %d %v", bytes, err)
	}
}

func TestLivepeer4KMemoryAdmissionOnSmallEdge(t *testing.T) {
	isolateProcessingOverrideState(t)
	var profiles []map[string]any
	for _, height := range []int{2160, 1440, 1080, 720, 480, 360} {
		profiles = append(profiles, map[string]any{"height": height, "bitrate": 1000000})
	}
	raw, err := json.Marshal([]map[string]any{{"process": "Livepeer", "target_profiles": profiles}, {"process": "Thumbs"}, {"process": "AV", "codec": "AAC"}, {"process": "AV", "codec": "opus"}})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := boundedProcessingMemoryConfig(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := processingMemoryEstimate(policy, mist.SourceMediaInfo{Width: 3840, Height: 2160})
	if err != nil {
		t.Fatal(err)
	}
	if err := setProcessingProcessOverride("processing+remote4k", processingSourceProbePolicy, "remote-job", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(1536) * memoryMiB
	if admitted, err := reserveProcessingMemory("processing+remote4k", "remote-job", policy, bytes, total, total); err != nil || !admitted {
		t.Fatalf("healthy Livepeer 4K job refused on 1.5 GiB: estimate=%d MiB admitted=%v err=%v", bytes/memoryMiB, admitted, err)
	}
}

func TestProcessingFallbackAcquiresLocalMemoryBeforePublishingPolicy(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+fallback-budget", "fallback-budget-job"
	remote := `[{"process":"Livepeer","target_profiles":[{"height":2160,"bitrate":8000000},{"height":1440,"bitrate":6000000},{"height":1080,"bitrate":4000000},{"height":720,"bitrate":2000000},{"height":480,"bitrate":1000000},{"height":360,"bitrate":600000}]},{"process":"Thumbs"}]`
	source := mist.SourceMediaInfo{Width: 3840, Height: 2160}
	remoteBytes, err := processingMemoryEstimate(remote, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = setProcessingProcessOverride(stream, remote, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(1536) * memoryMiB
	ok, err := reserveProcessingMemory(stream, job, remote, remoteBytes, total, total)
	if err != nil || !ok {
		t.Fatalf("remote reservation = %v, %v", ok, err)
	}
	h := &ProcessingJobHandler{memoryControls: map[string]bool{"AV": true, "Thumbs": true}, memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	request := &ipcpb.ProcessingJobRequest{JobId: job, ArtifactHash: "fallback-budget"}
	var published []*ipcpb.ControlMessage
	send := func(message *ipcpb.ControlMessage) { published = append(published, message) }
	local := mist.ReplaceLivepeerWithLocal(remote)
	_, err = h.admitProcessingPolicy(logrus.NewEntry(logrus.New()), request, stream, local, source, send)
	var capacity *processingCapacityError
	if !errors.As(err, &capacity) || !capacity.nodeTooSmall || len(published) != 0 {
		t.Fatalf("unreserved local policy: error=%v publications=%d", err, len(published))
	}
	processingProcessOverridesMu.Lock()
	retained := processingOverrideRecords[stream]
	processingProcessOverridesMu.Unlock()
	if retained.MemoryBytes != remoteBytes || retained.ProcessesJSON != remote {
		t.Fatalf("refused fallback changed its reservation: %+v", retained)
	}
	total = 8 * 1024 * memoryMiB
	admitted, err := h.admitProcessingPolicy(logrus.NewEntry(logrus.New()), request, stream, local, source, send)
	if err != nil || mist.HasLivepeerProcesses(admitted) || len(published) != 1 {
		t.Fatalf("admitted fallback: policy=%s error=%v publications=%d", admitted, err, len(published))
	}
	processingProcessOverridesMu.Lock()
	upgraded := processingOverrideRecords[stream]
	processingProcessOverridesMu.Unlock()
	if upgraded.MemoryBytes <= remoteBytes || upgraded.ProcessesJSON != admitted {
		t.Fatalf("local policy published without its larger durable reservation: %+v", upgraded)
	}
}

func TestProcessingMemoryReclaimsInactiveFileCache(t *testing.T) {
	limit := uint64(3072) * memoryMiB
	files := map[string]string{"/sys/fs/cgroup/memory.max": strconv.FormatUint(limit, 10), "/sys/fs/cgroup/memory.current": strconv.FormatUint(limit-100*memoryMiB, 10), "/sys/fs/cgroup/memory.stat": "anon 800000000\ninactive_file 2147483648\n"}
	total, available, err := readProcessingMemorySnapshot(limit, func(path string) ([]byte, error) {
		if s, ok := files[path]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	})
	if err != nil || total != limit || available != 2148*memoryMiB {
		t.Fatalf("reclaimable disk cache blocked admission: total=%d available=%d err=%v", total, available, err)
	}
}

func TestProcessingMemoryDoesNotForceLowQualityOnAmpleNode(t *testing.T) {
	policy, err := boundedProcessingMemoryConfig(`[{"process":"AV","codec":"H264","resolution":"3840x2160"}]`)
	if err != nil {
		t.Fatal(err)
	}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(policy), &processes); err != nil {
		t.Fatal(err)
	}
	if threads, ok := processes[0]["encode_threads"].(float64); !ok || threads <= 1 {
		t.Fatalf("ample node forced single-thread encoding: %s", policy)
	}
	if lookahead, ok := processes[0]["lookahead"].(float64); ok && lookahead == 0 {
		t.Fatalf("ample node disabled lookahead: %s", policy)
	}
}
