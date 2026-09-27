package control

import (
	"context"
	"errors"
	"fmt"
	"sync"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PlaybackGrantSessions is the edge's playback-grant store
// (internal/playbackgrant), installed by the handlers package.
type PlaybackGrantSessions interface {
	ApplyGrant(*ipcpb.PlaybackGrant)
	ControlConnected(context.Context)
	StopStreams(names []string, reason string)
	RecheckStreams(names []string) []string
}

var (
	playbackGrantSessionsMu sync.RWMutex
	playbackGrantSessions   PlaybackGrantSessions

	playbackGrantWaitersMu sync.Mutex
	playbackGrantWaiters   = map[string]chan *ipcpb.PlaybackGrantResponse{}
)

// SetPlaybackGrantSessions installs the store grants and control events are
// delivered to.
func SetPlaybackGrantSessions(sessions PlaybackGrantSessions) {
	playbackGrantSessionsMu.Lock()
	playbackGrantSessions = sessions
	playbackGrantSessionsMu.Unlock()
}

func currentPlaybackGrantSessions() PlaybackGrantSessions {
	playbackGrantSessionsMu.RLock()
	defer playbackGrantSessionsMu.RUnlock()
	return playbackGrantSessions
}

// RequestPlaybackGrant fetches the current grant for one Mist stream from
// Foghorn. An error means no grant can be issued (Foghorn holds no ready
// signed authority for the stream) or Foghorn cannot be reached.
func RequestPlaybackGrant(ctx context.Context, internalName string) (*ipcpb.PlaybackGrant, error) {
	stream := getStream()
	if stream == nil {
		return nil, errors.New("gRPC control stream not connected")
	}
	requestID := uuid.NewString()
	responseCh := make(chan *ipcpb.PlaybackGrantResponse, 1)
	playbackGrantWaitersMu.Lock()
	playbackGrantWaiters[requestID] = responseCh
	playbackGrantWaitersMu.Unlock()
	defer func() {
		playbackGrantWaitersMu.Lock()
		delete(playbackGrantWaiters, requestID)
		playbackGrantWaitersMu.Unlock()
	}()
	msg := &ipcpb.ControlMessage{
		RequestId: requestID,
		SentAt:    timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_PlaybackGrantRequest{PlaybackGrantRequest: &ipcpb.PlaybackGrantRequest{
			RequestId: requestID, InternalName: internalName,
		}},
	}
	if err := stream.Send(msg); err != nil {
		return nil, fmt.Errorf("send playback grant request: %w", err)
	}
	select {
	case resp := <-responseCh:
		if resp.GetGrant() == nil {
			return nil, fmt.Errorf("foghorn issued no playback grant: %s", resp.GetError())
		}
		return resp.GetGrant(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func handlePlaybackGrantResponse(resp *ipcpb.PlaybackGrantResponse) {
	if resp == nil || resp.GetRequestId() == "" {
		return
	}
	playbackGrantWaitersMu.Lock()
	ch, ok := playbackGrantWaiters[resp.GetRequestId()]
	playbackGrantWaitersMu.Unlock()
	if ok {
		offerResponse(ch, resp)
	}
}

// handlePlaybackGrant installs a pushed grant. It runs on the receive loop so
// a grant Foghorn sends before a PLAY_REWRITE response is in place when that
// response is handled.
func handlePlaybackGrant(grant *ipcpb.PlaybackGrant) {
	if sessions := currentPlaybackGrantSessions(); sessions != nil {
		sessions.ApplyGrant(grant)
	}
}

func notifyPlaybackGrantsConnected() {
	if sessions := currentPlaybackGrantSessions(); sessions != nil {
		go sessions.ControlConnected(context.Background())
	}
}
