package control

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

func TestIsolatedDVRWritersCannotReuseSegmentFilenamesAfterRollover(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "recording")
	var previous string
	for _, generation := range []string{"84ed1d31-13d2-468d-b7b5-ef132d4e3ee9", "faed1d31-13d2-468d-b7b5-ef132d4e3ee9"} {
		runtime, err := streamident.BoundRuntimeName("stream", streamident.RuntimePublisher, generation)
		if err != nil {
			t.Fatal(err)
		}
		client := &startAwareFakeMist{pushIDToReturn: 100}
		manager := newDVRManagerWithMist(t, client)
		job := &DVRJob{DVRHash: "recording", OutputDir: directory, SourceRuntimeName: runtime,
			Logger: logging.NewLogger(), Config: &ipcpb.DVRConfig{SegmentDuration: 6, DvrWindowSeconds: 12, MaxEntries: 2}}
		if err := manager.startDVRPush(job); err != nil {
			t.Fatal(err)
		}
		prefix := strings.ReplaceAll(generation, "-", "") + "_$minute_$segmentCounter.ts"
		if !strings.Contains(client.lastTargetURI, "/segments/"+prefix+"?") || client.lastTargetURI == previous {
			t.Fatalf("isolated DVR source generations share an output namespace: %s", client.lastTargetURI)
		}
		if !strings.Contains(client.lastTargetURI, "m3u8=../recording.m3u8") {
			t.Fatalf("writer rollover changed the recording manifest: %s", client.lastTargetURI)
		}
		previous = client.lastTargetURI
	}
}
