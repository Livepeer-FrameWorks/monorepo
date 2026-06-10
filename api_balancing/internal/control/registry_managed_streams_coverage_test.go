package control

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// TestApplyRedisChange_SourceUpsertCRDTMerge locks the per-Location CRDT merge
// decision: an inbound peer snapshot that is fresh for one cluster must NOT
// roll back a strictly-newer local Location (here the local cluster's
// SourceActive / OwnerNodeID admission state). Wholesale-replacing the entry
// on every pubsub upsert would silently drop duplicate-ingest protection, so
// the merge must keep the fresher local Location while still accepting the
// peer's own cluster Location.
func TestApplyRedisChange_SourceUpsertCRDTMerge(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-local", time.Minute)

	now := time.Now()
	// Seed a local entry whose local cluster Location is fresh and carries
	// live admission state (SourceActive owned by node-local).
	r.byInt["streamx"] = &cachedEntry{
		entry: StreamEntry{
			StreamID:     "sid-x",
			InternalName: "streamx",
			PlaybackID:   "play-x",
			Locations: map[string]Location{
				"cluster-local": {
					ClusterID:    "cluster-local",
					IsOrigin:     true,
					SourceActive: true,
					OwnerNodeID:  "node-local",
					UpdatedAt:    now,
				},
			},
		},
		cached: now,
	}

	// Incoming peer snapshot: STALE local Location (older UpdatedAt, no
	// SourceActive) plus a fresh peer-cluster Location.
	incoming := StreamEntry{
		StreamID:     "sid-x",
		InternalName: "streamx",
		Locations: map[string]Location{
			"cluster-local": {
				ClusterID: "cluster-local",
				UpdatedAt: now.Add(-time.Minute),
			},
			"cluster-peer": {
				ClusterID: "cluster-peer",
				IsLiveNow: true,
				UpdatedAt: now,
			},
		},
	}
	payload, err := json.Marshal(incoming)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	r.applyRedisChange(RegistryChange{
		InstanceID: "peer-instance",
		Entity:     RegistryEntitySource,
		Operation:  RegistryOpUpsert,
		Key:        "streamx",
		Payload:    payload,
	})

	ce, ok := r.byInt["streamx"]
	if !ok {
		t.Fatal("entry vanished after upsert")
	}
	local := ce.entry.Locations["cluster-local"]
	if !local.SourceActive || local.OwnerNodeID != "node-local" {
		t.Fatalf("fresher local Location was rolled back by stale peer snapshot: %+v", local)
	}
	peer, ok := ce.entry.Locations["cluster-peer"]
	if !ok || !peer.IsLiveNow {
		t.Fatalf("peer Location not merged in: %+v", ce.entry.Locations)
	}
	// Identity indexes must all point at the merged entry.
	if _, ok := r.byID["sid-x"]; !ok {
		t.Fatal("byID index not maintained after merge")
	}
	if _, ok := r.byPlay["play-x"]; !ok {
		t.Fatal("byPlay index (local-only identity) lost after merge")
	}
}

// TestApplyRedisChange_SourceUpsertNewEntry locks that a pubsub upsert for an
// unknown stream installs a fresh entry across all three identity indexes
// (byInt/byID/byPlay) so subsequent lookups by any reference resolve it.
func TestApplyRedisChange_SourceUpsertNewEntry(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-local", time.Minute)

	incoming := StreamEntry{
		StreamID:     "sid-new",
		InternalName: "streamnew",
		PlaybackID:   "play-new",
		IngestMode:   IngestPush,
		RuntimeName:  "live+streamnew",
		Locations: map[string]Location{
			"cluster-local": {ClusterID: "cluster-local", UpdatedAt: time.Now()},
		},
	}
	payload, err := json.Marshal(incoming)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	r.applyRedisChange(RegistryChange{
		Entity:    RegistryEntitySource,
		Operation: RegistryOpUpsert,
		Key:       "streamnew",
		Payload:   payload,
	})

	for _, idx := range []struct {
		name string
		m    map[string]*cachedEntry
		key  string
	}{
		{"byInt", r.byInt, "streamnew"},
		{"byID", r.byID, "sid-new"},
		{"byPlay", r.byPlay, "play-new"},
	} {
		if _, ok := idx.m[idx.key]; !ok {
			t.Fatalf("%s missing key %q after new-entry upsert", idx.name, idx.key)
		}
	}
}

// TestApplyRedisChange_StaleDeleteDroppedByTombstoneOrdering locks the tombstone
// ordering invariant: a peer delete published BEFORE the local entry was last
// updated must be dropped, because the stream was re-admitted locally after the
// delete was published. Honoring the stale delete would wipe live local
// SourceActive state. A delete with NO local Location fresher than its stamp
// (or no stamp) proceeds and removes every index.
func TestApplyRedisChange_StaleDeleteDroppedByTombstoneOrdering(t *testing.T) {
	t.Run("stale delete dropped", func(t *testing.T) {
		r := NewStreamRegistry(nil, "cluster-local", time.Minute)
		r.redisLogger = logging.NewLogger() // exercise the drop-debug-log arm

		localUpdate := time.Now()
		r.byInt["streamz"] = &cachedEntry{
			entry: StreamEntry{
				StreamID:     "sid-z",
				InternalName: "streamz",
				PlaybackID:   "play-z",
				Locations: map[string]Location{
					"cluster-local": {ClusterID: "cluster-local", SourceActive: true, UpdatedAt: localUpdate},
				},
			},
			cached: localUpdate,
		}
		r.byID["sid-z"] = r.byInt["streamz"]
		r.byPlay["play-z"] = r.byInt["streamz"]

		// Delete published a full minute BEFORE the local Location's UpdatedAt.
		r.applyRedisChange(RegistryChange{
			Entity:              RegistryEntitySource,
			Operation:           RegistryOpDelete,
			Key:                 "streamz",
			PublishedAtUnixNano: localUpdate.Add(-time.Minute).UnixNano(),
		})

		if _, ok := r.byInt["streamz"]; !ok {
			t.Fatal("stale delete wiped a fresher local entry; tombstone ordering broken")
		}
	})

	t.Run("fresh delete removes all indexes", func(t *testing.T) {
		r := NewStreamRegistry(nil, "cluster-local", time.Minute)

		localUpdate := time.Now().Add(-time.Minute)
		ce := &cachedEntry{
			entry: StreamEntry{
				StreamID:     "sid-z",
				InternalName: "streamz",
				PlaybackID:   "play-z",
				Locations: map[string]Location{
					"cluster-local": {ClusterID: "cluster-local", UpdatedAt: localUpdate},
				},
			},
			cached: localUpdate,
		}
		r.byInt["streamz"] = ce
		r.byID["sid-z"] = ce
		r.byPlay["play-z"] = ce

		// Delete published AFTER the local Location's UpdatedAt -> proceed.
		r.applyRedisChange(RegistryChange{
			Entity:              RegistryEntitySource,
			Operation:           RegistryOpDelete,
			Key:                 "streamz",
			PublishedAtUnixNano: time.Now().UnixNano(),
		})

		if _, ok := r.byInt["streamz"]; ok {
			t.Fatal("byInt not cleared after fresh delete")
		}
		if _, ok := r.byID["sid-z"]; ok {
			t.Fatal("byID not cleared after fresh delete (removeSourceByKeyLocked must drop derived indexes)")
		}
		if _, ok := r.byPlay["play-z"]; ok {
			t.Fatal("byPlay not cleared after fresh delete")
		}
	})
}

// fakeManagedMaterializer records the side-effects the reconciler is supposed
// to replay for an admitted managed stream: stream-context cache population and
// (gated on is_recording_enabled) auto-DVR start keyed by the SOURCE node.
type fakeManagedMaterializer struct {
	mu           sync.Mutex
	populated    []string // internal names passed to PopulateStreamContext
	dvrStreamIDs []string
	dvrNodeIDs   []string
}

func (f *fakeManagedMaterializer) PopulateStreamContext(streamCtx *commodorepb.ResolveStreamContextResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.populated = append(f.populated, streamCtx.GetInternalName())
}

func (f *fakeManagedMaterializer) EnsureManagedStreamDVR(_ context.Context, streamCtx *commodorepb.ResolveStreamContextResponse, sourceNodeID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dvrStreamIDs = append(f.dvrStreamIDs, streamCtx.GetStreamId())
	f.dvrNodeIDs = append(f.dvrNodeIDs, sourceNodeID)
}

// TestMaterializeManagedStream_RecordingEnabledStartsDVROnSourceNode locks the
// admitted-tick side-effect arm: when an admitted managed stream has recording
// enabled, the reconciler must replay the PUSH_REWRITE-equivalent auto-DVR
// start keyed by the ELECTED source node (mist_native bypasses PUSH_REWRITE, so
// this hook is the only DVR trigger). When recording is disabled the DVR hook
// must NOT fire, but the stream-context cache is still populated.
func TestMaterializeManagedStream_RecordingEnabledStartsDVROnSourceNode(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	prev := managedStreamMaterializer
	t.Cleanup(func() { managedStreamMaterializer = prev })

	t.Run("recording enabled starts DVR on source node", func(t *testing.T) {
		fake := &fakeManagedMaterializer{}
		managedStreamMaterializer = fake
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			streamContext: func(_ context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return &commodorepb.ResolveStreamContextResponse{
					Admitted:           true,
					StreamId:           req.GetStreamId(),
					InternalName:       "live+rec1",
					IsRecordingEnabled: true,
				}, nil
			},
		})

		_, st := materializeManagedStream(ctx, log, "c1", "node-source", &commodorepb.ManagedStreamRow{StreamId: "rec1"})
		if st != materializeOK {
			t.Fatalf("status = %v, want OK", st)
		}
		if len(fake.populated) != 1 || fake.populated[0] != "live+rec1" {
			t.Fatalf("PopulateStreamContext not called with stream context: %+v", fake.populated)
		}
		if len(fake.dvrNodeIDs) != 1 {
			t.Fatalf("EnsureManagedStreamDVR called %d times, want 1", len(fake.dvrNodeIDs))
		}
		if fake.dvrNodeIDs[0] != "node-source" {
			t.Fatalf("DVR keyed on node %q, want node-source (elected source node)", fake.dvrNodeIDs[0])
		}
		if fake.dvrStreamIDs[0] != "rec1" {
			t.Fatalf("DVR keyed on stream %q, want rec1", fake.dvrStreamIDs[0])
		}
	})

	t.Run("recording disabled does not start DVR", func(t *testing.T) {
		fake := &fakeManagedMaterializer{}
		managedStreamMaterializer = fake
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			streamContext: func(_ context.Context, _ *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return &commodorepb.ResolveStreamContextResponse{
					Admitted:           true,
					StreamId:           "rec2",
					InternalName:       "live+rec2",
					IsRecordingEnabled: false,
				}, nil
			},
		})

		_, st := materializeManagedStream(ctx, log, "c1", "node-source", &commodorepb.ManagedStreamRow{StreamId: "rec2"})
		if st != materializeOK {
			t.Fatalf("status = %v, want OK", st)
		}
		if len(fake.populated) != 1 {
			t.Fatalf("PopulateStreamContext should still run on admit: %+v", fake.populated)
		}
		if len(fake.dvrNodeIDs) != 0 {
			t.Fatalf("EnsureManagedStreamDVR must not fire when recording disabled: %+v", fake.dvrNodeIDs)
		}
	})
}
