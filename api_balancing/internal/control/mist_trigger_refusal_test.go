package control

import (
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/ingesterrors"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A viewer asking for an offline stream or a refused publisher is a decision
// about the request, logged at info; only processing faults log at error.
func TestMistTriggerRefusal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"offline stream", fmt.Errorf("playback placement admission failed for connector %q: %w", "HTTP", ErrLiveSourceOffline), true},
		{"starting stream", fmt.Errorf("resolve: %w", ErrLiveSourceStarting), true},
		{"invalid stream key", ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY, "invalid"), true},
		{"placement denied", fmt.Errorf("push: %w", ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED, "denied")), true},
		{"internal ingest error", ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL, "db down"), false},
		{"timeout", ingesterrors.New(ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, "slow"), false},
		{"untyped failure", errors.New("unexpected payload"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mistTriggerRefusal(tc.err); got != tc.want {
				t.Fatalf("mistTriggerRefusal = %v, want %v", got, tc.want)
			}
		})
	}
}
