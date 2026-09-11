package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func testInbound(node string) InboundPull {
	return InboundPull{TenantID: "tenant", SourceClusterID: "source-cluster", SourceNodeID: "origin", SourceGeneration: "generation-1", SourceRevision: 1, DestClusterID: "private-cluster", DestNodeID: node, DestNodeBaseURL: "https://" + node, DTSCURL: "dtsc://origin/live+stream"}
}

func TestReplicationDiagnosticCannotAuthorizeAPull(t *testing.T) {
	location := Location{DestNodeID: "edge", ReplicatingFrom: "source", PullSourceNodeID: "origin", PullDTSCURL: "dtsc://origin/live+stream"}
	if len(inboundPulls(location)) != 0 || len(activeInboundPulls(location)) != 0 {
		t.Fatal("diagnostic fields synthesized an unbound source attempt")
	}
}

// A renewal reissues the per-attempt source credential, and rotating the
// signing secret changes it outright. The physical pull is the same one, so
// identity is compared on the media path; comparing the credentialed URL would
// turn every rotation into a conflict that wedges the pull until the attempt
// aged out.
func TestInboundPullIdentitySurvivesSourceCredentialRotation(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	input := testInbound("edge")
	base := input.DTSCURL
	input.DTSCURL = base + "?token=fwsrc.attempt-1.first-signature"
	pull, err := r.RecordInboundPull(t.Context(), "stream", input)
	if err != nil {
		t.Fatal(err)
	}
	rotated := pull
	rotated.DTSCURL = base + "?token=fwsrc.attempt-1.rotated-signature"
	renewed, err := r.RecordInboundPull(t.Context(), "stream", rotated)
	if err != nil {
		t.Fatalf("rotated source credential was rejected as a conflicting pull: %v", err)
	}
	if renewed.AttemptID != pull.AttemptID {
		t.Fatalf("rotation replaced the physical attempt: %q -> %q", pull.AttemptID, renewed.AttemptID)
	}
	// A genuinely different media path is still a conflict.
	moved := pull
	moved.DTSCURL = "dtsc://other-origin/live+stream?token=fwsrc.attempt-1.first-signature"
	if _, err = r.RecordInboundPull(t.Context(), "stream", moved); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("a different source path was accepted as the same pull: %v", err)
	}
}

func TestInboundAcceptanceRenewalPersistsWithoutReplacingAttempt(t *testing.T) {
	store, _, _ := newTestRedis(t)
	replica := func(id string) *StreamRegistry {
		r := NewStreamRegistry(nil, "cluster-test", time.Minute)
		r.redisStore, r.instanceID = store, id
		return r
	}
	input := testInbound("edge")
	input.SourceAcceptedAt = time.Now().Add(-4 * time.Minute)
	pull, err := replica("first").RecordInboundPull(t.Context(), "stream", input)
	if err != nil {
		t.Fatal(err)
	}
	previous := pull.SourceAcceptedAt
	pull.SourceAcceptedAt = time.Now()
	renewed, err := replica("renew").RecordInboundPull(t.Context(), "stream", pull)
	if err != nil || !renewed.SourceAcceptedAt.Equal(pull.SourceAcceptedAt) || renewed.AttemptID != pull.AttemptID {
		t.Fatalf("renewal lost its acceptance time or physical attempt: %+v, %v", renewed, err)
	}
	pull.SourceAcceptedAt = previous
	if _, err = replica("late").RecordInboundPull(t.Context(), "stream", pull); err != nil {
		t.Fatal(err)
	}
	current, found, err := replica("reader").CurrentInboundPull(t.Context(), "stream", "edge")
	if err != nil || !found || !current.SourceAcceptedAt.Equal(renewed.SourceAcceptedAt) || current.AttemptID != renewed.AttemptID {
		t.Fatalf("stale acceptance replaced the durable renewal: %+v, %v", current, err)
	}
}

func TestInboundDestinationObservationIsNotSourceRevocation(t *testing.T) {
	ctx := context.Background()
	r := NewStreamRegistry(nil, "cell", time.Minute)
	input := testInbound("edge")
	input.DestinationObserved = true
	pull, err := r.RecordInboundPull(ctx, "stream", input)
	if err != nil || pull.DestinationObserved {
		t.Fatalf("arrangement asserted observed media: %+v, %v", pull, err)
	}
	if marked, markErr := r.MarkInboundDestinationObserved(ctx, "stream", "edge", pull.AttemptID); markErr != nil || !marked {
		t.Fatalf("observation failed: %v", markErr)
	}
	current, found, err := r.CurrentInboundPull(ctx, "stream", "edge")
	if err != nil || !found || !current.DestinationObserved || current.DTSCURL != pull.DTSCURL || current.AttemptID != pull.AttemptID {
		t.Fatalf("observation revoked or changed source: %+v, %v", current, err)
	}
	if _, err = r.MarkInboundDestinationObserved(ctx, "stream", "edge", pull.AttemptID); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("duplicate observation was published: %v", err)
	}
	warm, err := r.RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil || warm.AttemptID != pull.AttemptID || !warm.DestinationObserved {
		t.Fatalf("warm arrangement lost completed pull: %+v, %v", warm, err)
	}
	if cleared, clearErr := r.ClearInboundPull(ctx, "stream", "edge", pull.AttemptID); clearErr != nil || !cleared {
		t.Fatalf("explicit revocation failed: %v", clearErr)
	}
	if _, err = r.MarkInboundDestinationObserved(ctx, "stream", "edge", pull.AttemptID); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("late observation restored a revoked pull: %v", err)
	}
	input = testInbound("edge")
	input.SourceGeneration, input.SourceRevision = "generation-2", 2
	replacement, err := r.RecordInboundPull(ctx, "stream", input)
	if err != nil || replacement.AttemptID == pull.AttemptID || replacement.DestinationObserved {
		t.Fatalf("new source inherited old observation: %+v, %v", replacement, err)
	}
	if _, err = r.MarkInboundDestinationObserved(ctx, "stream", "edge", pull.AttemptID); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("old observation marked a replacement pull: %v", err)
	}
}

func TestInboundReplicationDestinationsAndAttemptFences(t *testing.T) {
	ctx := context.Background()
	r := NewStreamRegistry(nil, "cell", time.Minute)
	a, err := r.RecordInboundPull(ctx, "stream", testInbound("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.RecordInboundPull(ctx, "stream", testInbound("b"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.AllLocalReplications()["stream"]; len(got) != 2 || got[0].DestNodeID != "a" || got[1].DestNodeID != "b" {
		t.Fatalf("destinations lost or unordered: %+v", got)
	}
	if loc, ok := r.LocalReplicationForNode(ctx, "live+stream", "b"); !ok || loc.DestNodeID != "b" {
		t.Fatalf("wrong exact destination: %+v %v", loc, ok)
	}
	reused, err := r.RecordInboundPull(ctx, "stream", testInbound("a"))
	if err != nil || reused.AttemptID != a.AttemptID {
		t.Fatalf("retry not idempotent: %+v %v", reused, err)
	}
	if cleared, clearErr := r.ClearInboundPull(ctx, "stream", "a", a.AttemptID); !cleared || clearErr != nil {
		t.Fatalf("clear a: %v %v", cleared, clearErr)
	}
	if got, ok := r.InboundPullForNode("stream", "b"); !ok || got.AttemptID != b.AttemptID {
		t.Fatal("completion cleared a different destination")
	}
	a2, err := r.RecordInboundPull(ctx, "stream", testInbound("a"))
	if err != nil || a2.AttemptID == a.AttemptID {
		t.Fatalf("replacement attempt not distinct: %+v %v", a2, err)
	}
	if _, err := r.ClearInboundPull(ctx, "stream", "a", a.AttemptID); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("late completion cleared replacement: %v", err)
	}
	changedSource := testInbound("a")
	changedSource.SourceGeneration = "generation-2"
	if _, err := r.RecordInboundPull(ctx, "stream", changedSource); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("source generation silently replaced: %v", err)
	}
	snapshot := r.Snapshot()
	for i := range snapshot {
		delete(snapshot[i].Locations["cell"].InboundPulls, "a")
	}
	if _, ok := r.InboundPullForNode("stream", "a"); !ok {
		t.Fatal("snapshot mutation escaped into registry")
	}
}

func TestInboundReplicationRedisConcurrentWritersAndStaleSnapshots(t *testing.T) {
	store, _, _ := newTestRedis(t)
	ctx := context.Background()
	newReplica := func(id string) *StreamRegistry {
		r := NewStreamRegistry(nil, "cluster-test", time.Minute)
		r.redisStore, r.instanceID = store, id
		return r
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := newReplica(fmt.Sprintf("writer-%d", i)).RecordInboundPull(ctx, "stream", testInbound(fmt.Sprintf("node-%d", i)))
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	stale, found, err := store.GetSource(ctx, "stream")
	if err != nil || !found || len(activeInboundPulls(stale.Locations["cluster-test"])) != 8 {
		t.Fatalf("concurrent destination lost: %+v %v", stale, err)
	}
	pull := stale.Locations["cluster-test"].InboundPulls["node-0"]
	writer := newReplica("finisher")
	if cleared, clearErr := writer.ClearInboundPull(ctx, "stream", "node-0", pull.AttemptID); !cleared || clearErr != nil {
		t.Fatalf("durable clear: %v %v", cleared, clearErr)
	}
	change := RegistryChange{InstanceID: "stale", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: "stream"}
	if applied, writeErr := store.SetSourceRevisioned(ctx, stale, change, 0); !applied || writeErr != nil {
		t.Fatalf("stale metadata update: %v %v", applied, writeErr)
	}
	latest, _, err := store.GetSource(ctx, "stream")
	if err != nil || !latest.Locations["cluster-test"].InboundPulls["node-0"].Cleared || len(activeInboundPulls(latest.Locations["cluster-test"])) != 7 {
		t.Fatalf("stale snapshot resurrected or erased a destination: %+v %v", latest, err)
	}
	// Merging the same stale view through changelog replay must agree with restart.
	merged := mergeStreamEntry(latest, stale)
	if !merged.Locations["cluster-test"].InboundPulls["node-0"].Cleared {
		t.Fatal("changelog replay resurrected a cleared attempt")
	}
	replacement, err := writer.RecordInboundPull(ctx, "stream", testInbound("node-0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newReplica("late-finisher").ClearInboundPull(ctx, "stream", "node-0", pull.AttemptID); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("stale replica cleared replacement %s: %v", replacement.AttemptID, err)
	}
}

func TestInboundReplicationPersistenceFailureIsNotAccepted(t *testing.T) {
	store, _, server := newTestRedis(t)
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.redisStore = store
	server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.RecordInboundPull(ctx, "stream", testInbound("a")); err == nil {
		t.Fatal("unpersisted preparation accepted")
	}
	if _, ok := r.InboundPullForNode("stream", "a"); ok {
		t.Fatal("failed preparation became locally selectable")
	}
}

func TestInboundReplicationCompletedAttemptRetainsSourceFence(t *testing.T) {
	store, _, _ := newTestRedis(t)
	ctx := context.Background()
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.redisStore = store
	pull := testInbound("edge")
	pull.SourceGeneration, pull.SourceRevision = "generation-2", 2
	accepted, err := r.RecordInboundPull(ctx, "stream", pull)
	if err != nil {
		t.Fatal(err)
	}
	if _, clearErr := r.ClearInboundPull(ctx, "stream", "edge", accepted.AttemptID); clearErr != nil {
		t.Fatal(clearErr)
	}
	for name, stale := range map[string]InboundPull{
		"older_source":             testInbound("edge"),
		"completed_attempt":        accepted,
		"contradictory_generation": {TenantID: pull.TenantID, SourceClusterID: pull.SourceClusterID, SourceNodeID: pull.SourceNodeID, SourceGeneration: "another", SourceRevision: 2, DestClusterID: pull.DestClusterID, DestNodeID: pull.DestNodeID, DTSCURL: pull.DTSCURL},
	} {
		t.Run(name, func(t *testing.T) {
			restarted := NewStreamRegistry(nil, "cluster-test", time.Minute)
			restarted.redisStore = store
			if _, recordErr := restarted.RecordInboundPull(ctx, "stream", stale); !errors.Is(recordErr, ErrReplicationConflict) {
				t.Fatalf("completed source fence bypassed: %v", recordErr)
			}
		})
	}
	if next, recordErr := r.RecordInboundPull(ctx, "stream", pull); recordErr != nil || next.AttemptID == accepted.AttemptID {
		t.Fatalf("fresh attempt for current source refused: %+v %v", next, recordErr)
	}
}
