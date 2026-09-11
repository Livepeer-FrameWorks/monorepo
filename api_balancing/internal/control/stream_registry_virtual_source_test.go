package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInboundVirtualSourceRevalidationPreservesAttempt(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[shared], func(t *testing.T) {
			r := NewStreamRegistry(nil, "cluster-test", time.Minute)
			if shared {
				store, _, _ := newTestRedis(t)
				r.redisStore = store
			}
			ctx := context.Background()
			unbound := testInbound("edge")
			unbound.TenantID, unbound.DestClusterID = "", ""
			first, err := r.RecordInboundPull(ctx, "stream", unbound)
			if err != nil {
				t.Fatal(err)
			}
			bound := testInbound("edge")
			bound.SourceMediaClusterID = "virtual-a"
			bound.AttemptID = uuid.NewString()
			if _, bindErr := r.RecordInboundPull(ctx, "stream", bound); !errors.Is(bindErr, ErrReplicationConflict) {
				t.Fatalf("unbound pull was relabeled by a metadata upgrade: %v", bindErr)
			}
			bound.AttemptID = first.AttemptID
			upgraded, err := r.RecordInboundPull(ctx, "stream", bound)
			if err != nil || upgraded.SourceMediaClusterID != bound.SourceMediaClusterID || upgraded.TenantID != bound.TenantID ||
				upgraded.DestClusterID != bound.DestClusterID || upgraded.AttemptID != first.AttemptID || !upgraded.CreatedAt.Equal(first.CreatedAt) {
				t.Fatalf("same physical pull was not fully bound: %+v, %v", upgraded, err)
			}
			for _, cluster := range []string{"", "virtual-b"} {
				changed := upgraded
				changed.SourceMediaClusterID = cluster
				changed.AttemptID = uuid.NewString()
				if _, changeErr := r.RecordInboundPull(ctx, "stream", changed); !errors.Is(changeErr, ErrReplicationConflict) {
					t.Fatalf("bound source was erased or moved: %v", changeErr)
				}
			}
			newer := bound
			newer.AttemptID, newer.SourceGeneration, newer.SourceRevision = uuid.NewString(), "generation-2", 2
			newer.SourceMediaClusterID = "virtual-b"
			replaced, err := r.RecordInboundPull(ctx, "stream", newer)
			if err != nil || replaced.SourceMediaClusterID != "virtual-b" || replaced.AttemptID == first.AttemptID {
				t.Fatalf("new source generation could not replace binding: %+v, %v", replaced, err)
			}
		})
	}
}

func TestOutboundVirtualSourceBindingCannotBeMovedOrDowngraded(t *testing.T) {
	store, _, _ := newTestRedis(t)
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.redisStore = store
	ctx := context.Background()
	first, err := r.RecordOutboundPull(ctx, "stream", testOutbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	bound := first
	bound.SourceMediaClusterID = "virtual-source"
	upgraded, err := r.RecordOutboundPull(ctx, "stream", bound)
	if err != nil || upgraded.AttemptID != first.AttemptID || upgraded.SourceMediaClusterID != bound.SourceMediaClusterID || !upgraded.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("source revalidation did not retain the physical attempt: %+v, %v", upgraded, err)
	}
	for _, cluster := range []string{"", "different"} {
		for _, attempt := range []string{first.AttemptID, uuid.NewString()} {
			changed := upgraded
			changed.SourceMediaClusterID, changed.AttemptID = cluster, attempt
			if _, changeErr := r.RecordOutboundPull(ctx, "stream", changed); !errors.Is(changeErr, ErrReplicationConflict) {
				t.Fatalf("same physical source changed virtual identity: %v", changeErr)
			}
		}
	}
	entry, found, err := store.GetSource(ctx, "stream")
	if err != nil || !found || len(entry.Locations["cluster-test"].OutboundPullers) != 1 || entry.Locations["cluster-test"].OutboundPullers[0].SourceMediaClusterID != "virtual-source" {
		t.Fatalf("source-side virtual binding not persisted: %+v, %v", entry, err)
	}
}
