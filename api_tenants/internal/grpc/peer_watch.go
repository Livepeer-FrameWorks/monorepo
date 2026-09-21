package grpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// peerChangeInterval is how often the process re-reads the fingerprint of the
// tables a peer set is derived from. It is one small aggregate per interval for
// the whole process, not per subscriber, and it produces no traffic while
// nothing changes.
const peerChangeInterval = 2 * time.Second

// maxPeerWatchers bounds concurrent subscriptions. One media cell holds one
// stream, so this is far above any real fleet and exists to keep a
// misbehaving client from growing the hub without limit.
const maxPeerWatchers = 512

// peerWatchHub wakes subscribers when the peer census may have changed. It
// carries no peer data: a woken subscriber re-reads its own peer set, so a
// subscriber can never be handed another cluster's census.
type peerWatchHub struct {
	ctx     context.Context
	mu      sync.Mutex
	next    uint64
	waiters map[uint64]chan struct{}
}

func newPeerWatchHub() *peerWatchHub {
	return &peerWatchHub{ctx: context.Background(), waiters: make(map[uint64]chan struct{})}
}

// subscribe registers a waiter. The channel has room for one pending wake, so a
// burst of changes collapses into a single re-read rather than a queue of them.
func (hub *peerWatchHub) subscribe() (uint64, chan struct{}, error) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.ctx.Err() != nil {
		return 0, nil, status.Error(codes.Unavailable, "peer watch is draining")
	}
	if len(hub.waiters) >= maxPeerWatchers {
		return 0, nil, status.Error(codes.ResourceExhausted, "peer watch capacity reached")
	}
	hub.next++
	id := hub.next
	ch := make(chan struct{}, 1)
	hub.waiters[id] = ch
	return id, ch, nil
}

func (hub *peerWatchHub) unsubscribe(id uint64) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	delete(hub.waiters, id)
}

func (hub *peerWatchHub) wake() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, ch := range hub.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (hub *peerWatchHub) watcherCount() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.waiters)
}

// peerCensusFingerprint summarizes every input a peer set is derived from.
// Detecting change centrally keeps correctness independent of which handler
// performed a mutation, so a new write path cannot silently stop waking
// subscribers.
func (s *QuartermasterServer) peerCensusFingerprint(ctx context.Context) (string, error) {
	row, err := quartermasterdb.New(s.db).PeerCensusFingerprint(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d",
		row.AccessCount, row.AccessUpdatedAt.UTC().UnixNano(),
		row.ClusterCount, row.ClusterUpdatedAt.UTC().UnixNano(),
		row.InstanceCount, row.InstanceUpdatedAt.UTC().UnixNano(),
		row.AssignmentCount, row.AssignmentUpdatedAt.UTC().UnixNano()), nil
}

// RunPeerCensusWatcher wakes peer subscribers when the census changes. It holds
// no subscriber state itself, so it is safe to run with no watchers attached.
func (s *QuartermasterServer) RunPeerCensusWatcher(ctx context.Context, logger logging.Logger) {
	if s == nil || s.db == nil || s.peerWatch == nil {
		return
	}
	ticker := time.NewTicker(peerChangeInterval)
	defer ticker.Stop()
	previous := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.peerWatch.watcherCount() == 0 {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, peerChangeInterval)
		fingerprint, err := s.peerCensusFingerprint(readCtx)
		cancel()
		if err != nil {
			if logger != nil {
				logger.WithError(err).Warn("Peer census fingerprint unavailable; subscribers fall back to their own reconciliation")
			}
			continue
		}
		if previous == "" {
			previous = fingerprint
			continue
		}
		if fingerprint != previous {
			previous = fingerprint
			s.peerWatch.wake()
		}
	}
}

func (s *QuartermasterServer) WatchPeers(req *quartermasterpb.ListPeersRequest, stream quartermasterpb.ClusterService_WatchPeersServer) error {
	if err := requireServiceOrPlatformOperator(stream.Context(), "WatchPeers"); err != nil {
		return err
	}
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return status.Error(codes.InvalidArgument, "cluster_id required")
	}
	if s.peerWatch == nil {
		return status.Error(codes.Unavailable, "peer watch is not available")
	}
	id, wake, err := s.peerWatch.subscribe()
	if err != nil {
		return err
	}
	defer s.peerWatch.unsubscribe(id)

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	stop := context.AfterFunc(s.peerWatch.ctx, cancel)
	defer stop()
	// Returning the handler closes its transport stream, releasing a Send blocked
	// by client flow control. Cancelling only a derived context cannot do that.
	done := make(chan error, 1)
	go func() { done <- s.watchPeers(ctx, req, stream, wake) }()
	select {
	case <-s.peerWatch.ctx.Done():
		return status.Error(codes.Unavailable, "peer watch is draining")
	case <-stream.Context().Done():
		return stream.Context().Err()
	case err := <-done:
		if s.peerWatch.ctx.Err() != nil {
			return status.Error(codes.Unavailable, "peer watch is draining")
		}
		return err
	}
}

func (s *QuartermasterServer) watchPeers(ctx context.Context, req *quartermasterpb.ListPeersRequest, stream quartermasterpb.ClusterService_WatchPeersServer, wake <-chan struct{}) error {
	var last *quartermasterpb.ListPeersResponse
	send := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, listErr := s.ListPeers(ctx, req)
		if listErr != nil {
			return listErr
		}
		if last != nil && proto.Equal(last, current) {
			return nil
		}
		if sendErr := stream.Send(current); sendErr != nil {
			return sendErr
		}
		last = current
		return nil
	}
	// The first send is the current set, so a subscriber never has to pair the
	// stream with a separate initial read.
	if err := send(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
			if err := send(); err != nil {
				return err
			}
		}
	}
}
