package state

import (
	"context"
	"encoding/json"
	"fmt"
	goredis "github.com/redis/go-redis/v9"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewFallbackViewerUsesSamePlaybackIDOnAnotherNode(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint("primary-active-", active), func(t *testing.T) {
			sm := setupStateManager(t)
			configureTestNode(sm, "primary")
			configureTestNode(sm, "fallback")
			if ok, err := sm.PrepareViewerBandwidth(t.Context(), "playback", "primary", "stream"); !ok || err != nil {
				t.Fatal(ok, err)
			}
			if active {
				sm.StartVirtualViewerByID("playback", "primary", "stream", "203.0.113.1")
			}
			id, started := sm.StartVirtualViewerByID("playback", "fallback", "stream", "203.0.113.1")
			if id != "playback" || !started {
				t.Fatalf("fallback refused shared playback ID: id=%q started=%v", id, started)
			}
			if !sm.AttachVirtualViewerSession("playback", "fallback", "stream", "203.0.113.1", "mist-fallback") {
				t.Fatal("fallback USER_NEW could not attach")
			}
			if id, created := sm.StartVirtualViewerByID("playback", "fallback", "stream", "203.0.113.1"); id != "playback" || created {
				t.Fatalf("fallback retry duplicated viewer: %q %v", id, created)
			}
			if sm.GetNodeState("fallback").AddBandwidth == 0 {
				t.Fatal("fallback bypassed bandwidth reservation")
			}
			if !sm.DisconnectVirtualViewerBySessionID("mist-fallback", "fallback", "stream", "203.0.113.1") {
				t.Fatal("fallback USER_END could not match its session")
			}
		})
	}
}

func TestReviewConflictingStreamPreparationDoesNotReserveOtherNode(t *testing.T) {
	sm := setupStateManager(t)
	configureTestNode(sm, "a")
	configureTestNode(sm, "b")
	if ok, err := sm.PrepareViewerBandwidth(t.Context(), "same", "a", "stream"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := sm.PrepareViewerBandwidth(t.Context(), "same", "b", "other-stream"); ok || err != nil {
		t.Fatal("conflicting preparation accepted", ok, err)
	}
	if n := sm.GetNodeState("b"); n.AddBandwidth != 0 {
		t.Fatalf("failed preparation leaked %d bytes/s on other node", n.AddBandwidth)
	}
}

func TestReviewReservationUpdateDoesNotCreateUnknownNode(t *testing.T) {
	sm := setupStateManager(t)
	sm.applyBandwidthSums("deleted-peer-node", bandwidthSums{Egress: 123, Revision: 7}, false)
	if node := sm.GetNodeState("deleted-peer-node"); node != nil {
		t.Fatalf("reservation update recreated ghost node: id=%s updated=%v", node.NodeID, node.LastUpdate)
	}
}

func TestReviewSettlementWorkDoesNotScanPendingReservations(t *testing.T) {
	store, mr, _ := newRedisStateStore(t)
	now := time.Now()
	link := nodeLink(125000000, 0, 0)
	for i := range 100 {
		_, err := store.mutateBandwidth(context.Background(), "reserve", fmt.Sprint(i), "node", "replica", bandwidthReservation{Bytes: 1, Direction: BandwidthEgress, Stream: "stream", State: "PENDING", ExpiresMS: now.Add(time.Minute).UnixMilli()}, link, 0, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := mr.CommandCount()
	if _, err := store.mutateBandwidth(t.Context(), "settle", "", "node", "replica", bandwidthReservation{}, link, now.UnixMilli(), now); err != nil {
		t.Fatal(err)
	}
	if commands := mr.CommandCount() - before; commands > 25 {
		t.Fatalf("settling zero eligible reservations executed %d Redis commands for 100 pending viewers", commands)
	}
}

type reviewSlowBandwidthHook struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *reviewSlowBandwidthHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }
func (h *reviewSlowBandwidthHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}
func (h *reviewSlowBandwidthHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		if (cmd.Name() == "evalsha" || cmd.Name() == "eval") && strings.Contains(fmt.Sprint(cmd.Args()), "bw_resv:slow") {
			h.once.Do(func() { close(h.entered) })
			select {
			case <-h.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, cmd)
	}
}
func TestReviewIndependentNodesDoNotWaitForSlowRedisRoundTrip(t *testing.T) {
	store, _, client := newRedisStateStore(t)
	hook := &reviewSlowBandwidthHook{entered: make(chan struct{}), release: make(chan struct{})}
	client.AddHook(hook)
	sm := setupStateManager(t)
	configureTestNode(sm, "slow")
	configureTestNode(sm, "fast")
	sm.mu.Lock()
	sm.redisStore = store
	sm.instanceID = "replica"
	sm.mu.Unlock()
	slow := make(chan error, 1)
	go func() {
		_, err := sm.ReserveNodeBandwidth(t.Context(), "one", "slow", "stream", BandwidthEgress, 1)
		slow <- err
	}()
	select {
	case <-hook.entered:
	case <-time.After(time.Second):
		close(hook.release)
		t.Fatal("slow request never reached Redis")
	}
	fast := make(chan error, 1)
	go func() {
		_, err := sm.ReserveNodeBandwidth(t.Context(), "two", "fast", "stream", BandwidthEgress, 1)
		fast <- err
	}()
	fastFinished := false
	select {
	case err := <-fast:
		fastFinished = true
		if err != nil {
			t.Error(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("independent node queued behind slow Redis request")
	}
	close(hook.release)
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
	if !fastFinished {
		if err := <-fast; err != nil {
			t.Fatal(err)
		}
	}
}

func TestReviewFallbackPlayersBehindSameAddressStayDistinct(t *testing.T) {
	sm := setupStateManager(t)
	configureTestNode(sm, "a")
	configureTestNode(sm, "b")
	for _, id := range []string{"first", "second"} {
		if ok, err := sm.PrepareViewerBandwidth(t.Context(), id, "a", "stream"); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if got, started := sm.StartVirtualViewerByID(id, "b", "stream", "203.0.113.1"); got != id || !started {
			t.Fatalf("fallback merged player %q: %q %v", id, got, started)
		}
		if !sm.AttachVirtualViewerSession(id, "b", "stream", "203.0.113.1", "mist-"+id) {
			t.Fatal("session did not attach", id)
		}
	}
	if got := sm.ActiveVirtualViewerIDForSession("mist-second", "b", "stream"); got != "second" {
		t.Fatalf("second player attached to %q", got)
	}
	if !sm.DisconnectVirtualViewerBySessionID("mist-first", "b", "stream", "203.0.113.1") {
		t.Fatal("first player did not end")
	}
	if !sm.HasActiveVirtualViewerSession("mist-second", "b", "stream") {
		t.Fatal("first end removed second player")
	}
}

func TestReviewRefusalPreservesAdmittedConnectionAndAllowsRetry(t *testing.T) {
	sm := setupStateManager(t)
	configureTestNode(sm, "node")
	sm.StartVirtualViewerByID("playback", "node", "stream", "203.0.113.1")
	if !sm.AttachVirtualViewerSession("playback", "node", "stream", "203.0.113.1", "existing") {
		t.Fatal("session did not attach")
	}
	if sm.RejectUnconfirmedVirtualViewer("playback", "node", "stream") {
		t.Fatal("refusal abandoned an already admitted connection")
	}
	sm.DisconnectVirtualViewerBySessionID("existing", "node", "stream", "203.0.113.1")
	if id, started := sm.StartVirtualViewerByID("playback", "node", "stream", "203.0.113.1"); id != "playback" || !started {
		t.Fatalf("terminal viewer could not retry: %q %v", id, started)
	}
	if !sm.RejectUnconfirmedVirtualViewer("playback", "node", "stream") {
		t.Fatal("new attempt inherited previous Mist session")
	}
	if id, started := sm.StartVirtualViewerByID("playback", "node", "stream", "203.0.113.1"); id != "playback" || !started {
		t.Fatalf("refused viewer could not retry: %q %v", id, started)
	}
	if len(sm.GetVirtualViewersForNode("node")) != 1 {
		t.Fatal("retry duplicated node viewer index")
	}
}

func TestReviewRedisRecoveryPreservesActivationDuringOutage(t *testing.T) {
	store, server, client := newRedisStateStore(t)
	sm := setupStateManager(t)
	configureTestNode(sm, "node")
	sm.redisStore = store
	sm.instanceID = "replica"
	if allowed, err := sm.ReserveNodeBandwidth(t.Context(), "attempt", "node", "stream", BandwidthEgress, 100); !allowed || err != nil {
		t.Fatal(allowed, err)
	}
	server.SetError("LOADING temporary outage")
	if allowed, err := sm.ActivateNodeBandwidth(t.Context(), "attempt", "node"); !allowed || err != nil {
		t.Fatal(allowed, err)
	}
	server.SetError("")
	if allowed, err := sm.ReserveNodeBandwidth(t.Context(), "next", "node", "stream", BandwidthEgress, 1); !allowed || err != nil {
		t.Fatal(allowed, err)
	}
	raw, err := client.HGet(t.Context(), store.bandwidthKeys("node")[1], "r:attempt").Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var entry bandwidthReservation
	if err = json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.State != "HELD" || entry.ActivatedMS == 0 {
		t.Fatalf("Redis recovery forgot outage activation: %+v", entry)
	}
}
