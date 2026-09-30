package handlers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestRestreamPushEndMediaSelectionFailureOverridesDuration(t *testing.T) {
	parsed := mist.ParsePushEndStatus(`{"active_ms":5000,"media_tx":0,"reason_code":"media_selection_failed"}`)
	state, reason, message := restreamPushEndOutcome(parsed)
	if state != ipcpb.RestreamState_RESTREAM_STATE_FAILED ||
		reason != ipcpb.RestreamReason_RESTREAM_REASON_MEDIA_SELECTION_FAILED ||
		message != "no compatible video and audio tracks for this destination" {
		t.Fatalf("media selection push end = %s, %s, %q", state, reason, message)
	}
}
