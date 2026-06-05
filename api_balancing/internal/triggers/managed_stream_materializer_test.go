package triggers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// countingDVRStarter satisfies DVRStarter + DVRStarterWithSourceHint and
// records every Start invocation so tests can assert idempotency.
type countingDVRStarter struct {
	calls          atomic.Int32
	hintedCalls    atomic.Int32
	lastProcesses  atomic.Value // string
	lastSourceNode atomic.Value // string
}

func (c *countingDVRStarter) StartDVR(_ context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	c.calls.Add(1)
	c.lastProcesses.Store(req.GetProcessesJson())
	return &sharedpb.StartDVRResponse{}, nil
}

func (c *countingDVRStarter) StartDVRWithSourceHint(_ context.Context, req *sharedpb.StartDVRRequest, sourceNodeID string) (*sharedpb.StartDVRResponse, error) {
	c.hintedCalls.Add(1)
	c.calls.Add(1)
	c.lastProcesses.Store(req.GetProcessesJson())
	c.lastSourceNode.Store(sourceNodeID)
	return &sharedpb.StartDVRResponse{}, nil
}

func resetManagedDVRStarts(t *testing.T) {
	t.Helper()
	managedDVRStarts.Lock()
	managedDVRStarts.m = make(map[string]time.Time)
	managedDVRStarts.Unlock()
	t.Cleanup(func() {
		managedDVRStarts.Lock()
		managedDVRStarts.m = make(map[string]time.Time)
		managedDVRStarts.Unlock()
	})
}

func newMaterializerForTest(t *testing.T, dvr DVRStarter) *ManagedStreamMaterializer {
	t.Helper()
	p := &Processor{
		logger:     logging.NewLogger(),
		dvrService: dvr,
		streamCache: cache.New(cache.Options{
			TTL:        time.Minute,
			MaxEntries: 100,
		}, cache.MetricsHooks{}),
	}
	return p.ManagedStreamMaterializer()
}

func TestEnsureManagedStreamDVR_SuppressesRepeatWithinCooldown(t *testing.T) {
	resetManagedDVRStarts(t)
	starter := &countingDVRStarter{}
	m := newMaterializerForTest(t, starter)

	ctx := &commodorepb.ResolveStreamContextResponse{
		Admitted:           true,
		StreamId:           "stream-1",
		InternalName:       "internal-1",
		TenantId:           "tenant-1",
		IsRecordingEnabled: true,
		DvrProcessesJson:   `[{"process":"AV","codec":"AAC","track_select":"audio=all&video=none&subtitle=none"},{"process":"Thumbs","track_select":"video=maxbps"}]`,
	}
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-a")
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-a")

	if got := starter.calls.Load(); got != 1 {
		t.Fatalf("repeat call within cooldown should suppress; want 1 StartDVR, got %d", got)
	}
	if got := starter.hintedCalls.Load(); got != 1 {
		t.Fatalf("source-hint variant should be used when available; want 1, got %d", got)
	}
	if got := starter.lastSourceNode.Load(); got != "edge-a" {
		t.Fatalf("source_node_id not forwarded; want edge-a, got %v", got)
	}
	if got := starter.lastProcesses.Load(); got != ctx.GetDvrProcessesJson() {
		t.Fatalf("DVR processes_json should be forwarded exactly from Commodore; want %s, got %v", ctx.GetDvrProcessesJson(), got)
	}
}

func TestEnsureManagedStreamDVR_DifferentSourceNodeBypassesCooldown(t *testing.T) {
	resetManagedDVRStarts(t)
	starter := &countingDVRStarter{}
	m := newMaterializerForTest(t, starter)

	ctx := &commodorepb.ResolveStreamContextResponse{
		Admitted:           true,
		StreamId:           "stream-1",
		InternalName:       "internal-1",
		TenantId:           "tenant-1",
		IsRecordingEnabled: true,
	}
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-a")
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-b")

	if got := starter.calls.Load(); got != 2 {
		t.Fatalf("placement change to a new source node must re-Start; want 2 StartDVR, got %d", got)
	}
}

func TestEnsureManagedStreamDVR_SkipsWhenNotAdmittedOrNotRecording(t *testing.T) {
	resetManagedDVRStarts(t)
	starter := &countingDVRStarter{}
	m := newMaterializerForTest(t, starter)

	m.EnsureManagedStreamDVR(context.Background(), &commodorepb.ResolveStreamContextResponse{
		Admitted:           false,
		StreamId:           "stream-1",
		InternalName:       "internal-1",
		IsRecordingEnabled: true,
	}, "edge-a")
	m.EnsureManagedStreamDVR(context.Background(), &commodorepb.ResolveStreamContextResponse{
		Admitted:           true,
		StreamId:           "stream-2",
		InternalName:       "internal-2",
		IsRecordingEnabled: false,
	}, "edge-a")

	if got := starter.calls.Load(); got != 0 {
		t.Fatalf("denied/no-record paths must not Start; got %d", got)
	}
}

func TestPopulateStreamContext_WritesPushRewriteEquivalentCacheKeys(t *testing.T) {
	m := newMaterializerForTest(t, &countingDVRStarter{})

	m.PopulateStreamContext(&commodorepb.ResolveStreamContextResponse{
		Admitted:      true,
		StreamId:      "stream-1",
		InternalName:  "internal-1",
		TenantId:      "tenant-1",
		BillingModel:  "postpaid",
		ProcessesJson: `[{"type":"thumbnail","interval_seconds":10}]`,
	})

	if _, ok := m.p.streamCache.Peek("tenant-1:internal-1"); !ok {
		t.Fatalf("primary cache key tenantId:internalName not written")
	}
	if _, ok := m.p.streamCache.Peek("process:internal-1"); !ok {
		t.Fatalf("secondary cache key process:internalName not written")
	}
}

// failingDVRStarter rejects every Start call. Used to assert that a
// transient DVR failure clears the cooldown so the next reconciler tick
// retries instead of being silently suppressed for the full 5 minutes.
type failingDVRStarter struct {
	calls atomic.Int32
}

func (f *failingDVRStarter) StartDVR(_ context.Context, _ *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	f.calls.Add(1)
	return nil, errFakeDVR
}

func (f *failingDVRStarter) StartDVRWithSourceHint(_ context.Context, _ *sharedpb.StartDVRRequest, _ string) (*sharedpb.StartDVRResponse, error) {
	f.calls.Add(1)
	return nil, errFakeDVR
}

var errFakeDVR = errFakeError("fake dvr service error")

type errFakeError string

func (e errFakeError) Error() string { return string(e) }

func TestEnsureManagedStreamDVR_FailureClearsCooldown(t *testing.T) {
	resetManagedDVRStarts(t)
	starter := &failingDVRStarter{}
	m := newMaterializerForTest(t, starter)

	ctx := &commodorepb.ResolveStreamContextResponse{
		Admitted:           true,
		StreamId:           "stream-1",
		InternalName:       "internal-1",
		TenantId:           "tenant-1",
		IsRecordingEnabled: true,
	}
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-a")
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-a")
	m.EnsureManagedStreamDVR(context.Background(), ctx, "edge-a")

	if got := starter.calls.Load(); got != 3 {
		t.Fatalf("transient DVR failure must clear cooldown so each tick retries; want 3 calls, got %d", got)
	}
}

func TestPopulateStreamContext_NoOpWhenNotAdmitted(t *testing.T) {
	m := newMaterializerForTest(t, &countingDVRStarter{})

	m.PopulateStreamContext(&commodorepb.ResolveStreamContextResponse{
		Admitted:     false,
		StreamId:     "stream-1",
		InternalName: "internal-1",
		TenantId:     "tenant-1",
	})

	if _, ok := m.p.streamCache.Peek("tenant-1:internal-1"); ok {
		t.Fatalf("denied admission must not write any cache entry")
	}
}
