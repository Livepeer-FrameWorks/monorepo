package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// The union Viewers count is derived at read time by summing per-node
// instances (single-writer state); the stored union field is never served.
// See docs/architecture/foghorn-ha.md, "Single-writer vs multi-writer state".

func TestUnionViewers_DerivedFromInstances(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateUserConnection("s1", "node-a", "tenant-1", 1)
	sm.UpdateUserConnection("s1", "node-a", "tenant-1", 1)
	sm.UpdateUserConnection("s1", "node-b", "tenant-1", 1)

	if ss := sm.GetStreamState("s1"); ss == nil || ss.Viewers != 3 {
		t.Fatalf("union viewers = %+v, want 3 across two instances", ss)
	}

	// Per-instance decrements clamp at zero and the union follows.
	sm.UpdateUserConnection("s1", "node-b", "tenant-1", -5)
	if ss := sm.GetStreamState("s1"); ss == nil || ss.Viewers != 2 {
		t.Fatalf("union viewers after clamped decrement = %+v, want 2", ss)
	}
}

// UpdateNodeStats records per-node observations; the union aggregates sum
// them instead of last-write-wins overwriting across nodes.
func TestUnionStats_DerivedFromInstances(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateUserConnection("s1", "node-a", "tenant-1", 0) // seed tenant identity
	sm.UpdateNodeStats("s1", "node-a", 4, 1, 1000, 100, false)
	sm.UpdateNodeStats("s1", "node-b", 2, 1, 500, 50, true)

	ss := sm.GetStreamState("s1")
	if ss == nil {
		t.Fatal("stream missing")
	}
	if ss.TotalConnections != 6 || ss.Inputs != 2 || ss.BytesUp != 1500 || ss.BytesDown != 150 {
		t.Fatalf("union stats = total=%d inputs=%d up=%d down=%d, want sums 6/2/1500/150",
			ss.TotalConnections, ss.Inputs, ss.BytesUp, ss.BytesDown)
	}

	// GetStreamsByTenant's live-input filter uses the derived inputs.
	if got := sm.GetStreamsByTenant("tenant-1"); len(got) != 1 || got[0].Inputs != 2 {
		t.Fatalf("GetStreamsByTenant = %+v, want one stream with derived inputs=2", got)
	}
}

// A peer's stream snapshot carries a stored union counter the publishing
// instance computed; receivers must re-derive from instances rather than
// serve it (last snapshot wins would drop the other writer's viewers).
func TestUnionViewers_IncomingSnapshotValueNotServed(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Local truth: one viewer on node-a.
	sm.UpdateUserConnection("s1", "node-a", "tenant-1", 1)

	bogus := StreamState{InternalName: "s1", StreamName: "s1", Viewers: 999, Status: "live"}
	payload, _ := json.Marshal(bogus)
	sm.handleStateChangelogEntry("100-0", StateChange{
		InstanceID: "peer",
		Entity:     StateEntityStream,
		Operation:  StateOpUpsert,
		StreamName: "s1",
		Payload:    payload,
	})

	if ss := sm.GetStreamState("s1"); ss == nil || ss.Viewers != 1 {
		t.Fatalf("viewers = %+v, want locally derived 1 (stored 999 must not be served)", ss)
	}

	// Forward-compat: a payload without the viewers key is equally harmless.
	sm.handleStateChangelogEntry("101-0", StateChange{
		InstanceID: "peer",
		Entity:     StateEntityStream,
		Operation:  StateOpUpsert,
		StreamName: "s1",
		Payload:    json.RawMessage(`{"internal_name":"s1","stream_name":"s1","status":"live","last_update":"2026-01-01T00:00:00Z"}`),
	})
	if ss := sm.GetStreamState("s1"); ss == nil || ss.Viewers != 1 {
		t.Fatalf("viewers after field-less snapshot = %+v, want 1", ss)
	}
}

// Two HA instances each own a different node's viewers; both must converge
// to the same union sum. On pre-derivation code this fails: each instance's
// published union snapshot overwrites the other's count.
func TestUnionViewers_TwoInstanceConvergence(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	newInstance := func(id string) *StreamStateManager {
		sm := NewStreamStateManager()
		client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		store := NewRedisStateStore(client, "test-cluster")
		if err := sm.EnableRedisSync(context.Background(), store, id, logger); err != nil {
			t.Fatalf("EnableRedisSync %s: %v", id, err)
		}
		t.Cleanup(sm.Shutdown)
		return sm
	}
	smA := newInstance("instance-a")
	smB := newInstance("instance-b")

	// A owns node-a's conn stream, B owns node-b's.
	smA.UpdateUserConnection("s1", "node-a", "tenant-1", 2)
	smB.UpdateUserConnection("s1", "node-b", "tenant-1", 1)

	deadline := time.Now().Add(3 * time.Second)
	for {
		a, b := smA.GetStreamState("s1"), smB.GetStreamState("s1")
		if a != nil && b != nil && a.Viewers == 3 && b.Viewers == 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("union did not converge to 3: A=%+v B=%+v", a, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
