package control

import (
	"sync"
	"time"

	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
)

// RelayCacheDeleteWindow is how long after an artifact's deletion its delete
// keeps being sent to nodes that have not yet received it, such as a node that
// was offline when the artifact was deleted.
const RelayCacheDeleteWindow = 24 * time.Hour

// fanOutHeartbeatWindow bounds a fan-out to nodes that heartbeated recently.
const fanOutHeartbeatWindow = 2 * time.Minute

var (
	fanOutNodeIDs = func() []string {
		return state.DefaultManager().HeartbeatingNodeIDs(fanOutHeartbeatWindow)
	}
	fanOutSendVod  = SendVodDelete
	fanOutSendClip = SendClipDelete
	fanOutNow      = time.Now
	fanOutDelivery = &fanOutLedger{sent: map[string]*fanOutEntry{}}
)

// fanOutLedger records, per deleted artifact, the nodes this replica already
// delivered its delete to, so the repeated fan-out passes reach each node once.
type fanOutLedger struct {
	mu   sync.Mutex
	sent map[string]*fanOutEntry
}

type fanOutEntry struct {
	first time.Time
	nodes map[string]struct{}
}

// ArtifactDeleteFanOutNodeIDs returns the nodes a fan-out addresses.
func ArtifactDeleteFanOutNodeIDs() []string {
	return fanOutNodeIDs()
}

// SendArtifactDeleteToClusterNodes sends the delete command for a clip or VOD
// to every heartbeating node except skip and the nodes it already reached. The
// read-through relay caches an artifact on any edge that served or processed it
// (.blocks, .partial, .dtsh, upload/ inputs) without that edge reporting the
// artifact in its inventory, so the catalog's holder list does not name every
// node with bytes of it. The node delete is idempotent and finds nothing on a
// node without copies. artifactType "chapter" is a DVR chapter, stored as a
// VOD. Returns the per-node send failures; a failed node is tried again by the
// next call for the same artifact.
func SendArtifactDeleteToClusterNodes(artifactType, hash string, skip []string) map[string]error {
	if hash == "" || (artifactType != "clip" && artifactType != "vod" && artifactType != "chapter") {
		return nil
	}
	skipped := make(map[string]struct{}, len(skip))
	for _, id := range skip {
		skipped[id] = struct{}{}
	}
	var targets []string
	for _, nodeID := range fanOutNodeIDs() {
		if _, ok := skipped[nodeID]; ok || nodeID == "" || fanOutDelivery.delivered(hash, nodeID) {
			continue
		}
		targets = append(targets, nodeID)
	}
	if len(targets) == 0 {
		return nil
	}
	requestID := uuid.NewString()
	failures := map[string]error{}
	for _, nodeID := range targets {
		var err error
		if artifactType == "clip" {
			err = fanOutSendClip(nodeID, &ipcpb.ClipDeleteRequest{ClipHash: hash, RequestId: requestID})
		} else {
			err = fanOutSendVod(nodeID, &ipcpb.VodDeleteRequest{VodHash: hash, RequestId: requestID})
		}
		if err != nil {
			failures[nodeID] = err
			continue
		}
		fanOutDelivery.record(hash, nodeID)
	}
	return failures
}

func (l *fanOutLedger) delivered(hash, nodeID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.sent[hash]
	if e == nil {
		return false
	}
	_, ok := e.nodes[nodeID]
	return ok
}

// record notes a delivery and drops entries older than RelayCacheDeleteWindow,
// which the fan-out pass no longer lists.
func (l *fanOutLedger) record(hash, nodeID string) {
	now := fanOutNow()
	l.mu.Lock()
	defer l.mu.Unlock()
	for h, e := range l.sent {
		if now.Sub(e.first) > RelayCacheDeleteWindow {
			delete(l.sent, h)
		}
	}
	e := l.sent[hash]
	if e == nil {
		e = &fanOutEntry{first: now, nodes: map[string]struct{}{}}
		l.sent[hash] = e
	}
	e.nodes[nodeID] = struct{}{}
}
