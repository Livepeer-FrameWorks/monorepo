package federation

import (
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"reflect"
	"testing"
)

// wireTestWriters gives every connected mock peer a mailbox (so enqueue has
// somewhere to put frames) and returns a flush that synchronously drains each
// mailbox into its capture stream. It stands in for the per-peer writer goroutine
// connectPeer starts in production, without test goroutines or timing — call it
// after the peers are set up and call the returned flush before asserting on the
// capture streams.
func (pm *PeerManager) wireTestWriters() func() {
	pm.mu.Lock()
	for _, ps := range pm.peers {
		if ps.connected && ps.stream != nil && ps.sendCh == nil {
			ps.sendCh = make(chan *foghornfederationpb.PeerMessage, peerSendQueueSize)
		}
	}
	pm.mu.Unlock()

	return func() {
		pm.mu.RLock()
		defer pm.mu.RUnlock()
		for _, ps := range pm.peers {
			drainPeerMailbox(ps)
		}
	}
}

func drainPeerMailbox(ps *peerState) {
	if ps.sendCh == nil || ps.stream == nil {
		return
	}
	for {
		select {
		case msg := <-ps.sendCh:
			_ = ps.stream.Send(msg)
		default:
			return
		}
	}
}

// TestEnqueue_DropOldestUnderBackpressure locks the backpressure policy: when the
// mailbox is full, the OLDEST queued frame is evicted so the freshest work wins.
// Every federation frame is best-effort with its own backstop, so latest-wins is
// safe; dropping the newest would deliver stale summaries/heartbeats late.
func TestEnqueue_DropOldestUnderBackpressure(t *testing.T) {
	pm := newTestPeerManager(t, "local", nil, false)
	ps := &peerState{connected: true, stream: &capturePeerChannelStream{}, sendCh: make(chan *foghornfederationpb.PeerMessage, 2)}

	mk := func(label string) *foghornfederationpb.PeerMessage {
		return &foghornfederationpb.PeerMessage{ClusterId: label}
	}
	pm.enqueue("p", ps, mk("a")) // [a]
	pm.enqueue("p", ps, mk("b")) // [a,b] — full
	pm.enqueue("p", ps, mk("c")) // full → evict a → [b,c]

	var got []string
	for len(ps.sendCh) > 0 {
		got = append(got, (<-ps.sendCh).ClusterId)
	}
	if want := []string{"b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("drop-oldest: queue = %v, want %v (oldest 'a' should be evicted)", got, want)
	}
	if n := ps.dropped.Load(); n != 1 {
		t.Fatalf("expected dropped=1, got %d", n)
	}
}
