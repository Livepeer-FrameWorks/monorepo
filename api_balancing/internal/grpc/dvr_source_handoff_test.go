package grpc

import (
	"encoding/json"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/jobs"
	"frameworks/api_balancing/internal/state"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestDVRHandoffDoesNotHideUnreadableDispatchOrUnavailableWriter(t *testing.T) {
	server := &FoghornGRPCServer{}
	request := &sharedpb.StartDVRRequest{InternalName: "stream", IngestGeneration: "next"}
	if prepared, err := server.prepareDVRSourceHandoff(t.Context(), request, "source", "recording", "{"); prepared || err == nil {
		t.Errorf("unreadable dispatch silently fell back to adoption: prepared=%v err=%v", prepared, err)
	}
	manager := state.ResetDefaultManagerForTests()
	t.Cleanup(manager.Shutdown)
	const node = "handoff-disconnected-storage"
	manager.SetNodeInfo(node, "", true, nil, nil, "", "", nil)
	manager.SetNodeConnectionInfo(t.Context(), node, "", "tenant", "cell", nil)
	raw, err := json.Marshal(jobs.DVRStartDispatch{SourceNodeID: "source", NodeID: node, IngestGeneration: "previous",
		SourceRuntimeName: "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared, prepareErr := server.prepareDVRSourceHandoff(t.Context(), request, "source", "recording", string(raw)); prepared || !errors.Is(prepareErr, control.ErrDestinationNotConnected) {
		t.Errorf("missing writer capability silently fell back to adoption: prepared=%v err=%v", prepared, prepareErr)
	}
}
