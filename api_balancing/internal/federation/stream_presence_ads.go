package federation

import (
	"strconv"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// streamPresenceAdvertiser sends a stream's advertisement the moment its origin
// becomes placement-present, instead of leaving peers to wait for the periodic
// push. A peer can place viewers on a pushed stream only once it holds the
// origin's confirmed publisher generation, and the StreamAdvertisement is the
// only frame that carries it.
//
// Placement-present means one local instance has inputs, is not a replica, is
// playable, and holds the confirmed publisher binding for its node. That state
// is assembled from two stores that each replicate across the cell's Foghorn
// replicas, so the advertiser observes both stores rather than the trigger
// handlers: whichever replica handled STREAM_BUFFER, the Helmsman playability
// report, or the publisher binding, the leader sees the change when it applies
// the changelog entry.
//
// Change notifications coalesce per stream in pending, and advertised holds the
// presence (node, generation, revision) each stream was last advertised for, so
// a stream is sent at most once per presence transition however many state
// writes arrive. The periodic push remains the refresh and the backstop.
type streamPresenceAdvertiser struct {
	pm       *PeerManager
	sm       *state.StreamStateManager
	registry *control.StreamRegistry

	mu      sync.Mutex
	pending map[string]struct{}
	wake    chan struct{}

	// advertised is owned by the run goroutine.
	advertised map[string]string
}

// WatchStreamPresence starts advertising streams to peers as soon as their
// origin becomes placement-present in sm and registry. Only the PeerManager
// leader sends; other replicas drop their notifications. The returned function
// stops the watch; Close stops it as well.
func (pm *PeerManager) WatchStreamPresence(sm *state.StreamStateManager, registry *control.StreamRegistry) (stop func()) {
	if pm == nil || sm == nil || registry == nil {
		return func() {}
	}
	a := &streamPresenceAdvertiser{
		pm:         pm,
		sm:         sm,
		registry:   registry,
		pending:    make(map[string]struct{}),
		wake:       make(chan struct{}, 1),
		advertised: make(map[string]string),
	}
	removeState := sm.AddStreamChangeObserver(a.notify)
	removeRegistry := registry.AddSourceChangeObserver(a.notify)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.run(stopCh)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			removeState()
			removeRegistry()
			close(stopCh)
			<-done
		})
	}
}

// notify runs on the state or registry writer's goroutine, so it only records
// the stream and wakes the run loop.
func (a *streamPresenceAdvertiser) notify(internalName string) {
	a.mu.Lock()
	a.pending[internalName] = struct{}{}
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *streamPresenceAdvertiser) run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-a.pm.done:
			return
		case <-a.wake:
		}
		a.flush()
	}
}

func (a *streamPresenceAdvertiser) flush() {
	a.mu.Lock()
	names := a.pending
	a.pending = make(map[string]struct{})
	a.mu.Unlock()

	if !a.pm.IsLeader() {
		// A replica that later becomes leader starts from nothing advertised, so
		// its first observation of an already-present origin is sent.
		clear(a.advertised)
		return
	}

	due := make(map[string]string)
	for name := range names {
		key := a.presenceKey(name)
		switch {
		case key == "":
			delete(a.advertised, name)
		case a.advertised[name] != key:
			due[name] = key
		}
	}
	if len(due) == 0 {
		return
	}

	snapshot := a.sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return
	}
	streamNames := make([]string, 0, len(due))
	for name := range due {
		streamNames = append(streamNames, name)
	}
	dvrRecordingNodes := a.pm.lookupDVRRecordingNodes(streamNames)
	now := time.Now()
	messages := make([]*foghornfederationpb.PeerMessage, 0, len(due))
	for name, key := range due {
		entry, _ := a.registry.CachedSource(name)
		ad := a.pm.buildStreamAd(a.sm, snapshot, name, entry, now)
		// The balancer snapshot can lag the instance write that made the stream
		// present (the node may not be active in it yet). Such a stream stays
		// unadvertised here and is retried on its next change or the periodic push.
		if ad == nil || !advertisesPresence(ad, key) {
			continue
		}
		ad.DvrRecordingNodeId = dvrRecordingNodes[name]
		messages = append(messages, a.pm.streamAdMessage(ad))
		a.advertised[name] = key
	}
	if len(messages) == 0 {
		return
	}
	a.pm.mu.RLock()
	a.pm.sendStreamAdsLocked(messages)
	a.pm.mu.RUnlock()
}

// presenceKey identifies the placement-present origin of a stream as
// node|generation|revision, or "" when no local instance is present. It applies
// the same origin and binding rules buildStreamAd uses for IsOrigin and
// SourceGeneration, from per-stream reads only.
func (a *streamPresenceAdvertiser) presenceKey(internalName string) string {
	ss := a.sm.GetStreamState(internalName)
	if ss == nil || ss.Status != "live" {
		return ""
	}
	entry, found := a.registry.CachedSource(internalName)
	if !found {
		return ""
	}
	for nodeID, instance := range a.sm.GetStreamInstances(internalName) {
		if instance.TenantID != ss.TenantID || instance.Status != "live" || instance.Inputs <= 0 || instance.Replicated || !instance.Playable {
			continue
		}
		generation, revision := confirmedPublisherBinding(entry, nodeID, ss.TenantID, a.pm.clusterID)
		if generation == "" {
			continue
		}
		return presenceKeyFor(nodeID, generation, revision)
	}
	return ""
}

func presenceKeyFor(nodeID, generation string, revision int64) string {
	return nodeID + "|" + generation + "|" + strconv.FormatInt(revision, 10)
}

func advertisesPresence(ad *foghornfederationpb.StreamAdvertisement, key string) bool {
	for _, edge := range ad.GetEdges() {
		if edge.GetIsOrigin() && edge.GetPlayable() && edge.GetSourceGeneration() != "" &&
			presenceKeyFor(edge.GetNodeId(), edge.GetSourceGeneration(), edge.GetSourceRevision()) == key {
			return true
		}
	}
	return false
}
