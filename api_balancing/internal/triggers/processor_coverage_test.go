package triggers

import (
	"context"
	"sync"
	"testing"
	"time"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestGetClusterPeers_CacheHitMissAndValidation pins the demand-driven peer
// discovery lookup contract: peers are keyed by "tenant:internal", a hit
// returns exactly the cached ClusterPeers slice, a miss (and any
// empty-identifier input) returns nil so the caller falls through to a fresh
// resolve rather than peering against a stale/foreign tenant's clusters.
func TestGetClusterPeers_CacheHitMissAndValidation(t *testing.T) {
	p := newTestProcessor(t)
	tenantID := "tenant-1"
	internalName := "stream-x"
	peers := []*clusterpeerpb.TenantClusterPeer{
		{ClusterId: "media-eu-1"},
		{ClusterId: "media-us-1"},
	}
	p.streamCache.Set(tenantID+":"+internalName, streamContext{
		TenantID:     tenantID,
		ClusterPeers: peers,
	}, time.Minute)

	// Cache hit returns the cached peers for the matching tenant+stream.
	got := p.GetClusterPeers(internalName, tenantID)
	if len(got) != 2 || got[0].GetClusterId() != "media-eu-1" || got[1].GetClusterId() != "media-us-1" {
		t.Fatalf("cache hit returned %#v, want the 2 cached peers", got)
	}

	// Miss: present tenant but a stream that was never cached.
	if got := p.GetClusterPeers("other-stream", tenantID); got != nil {
		t.Fatalf("cache miss should return nil, got %#v", got)
	}

	// Tenant-isolation: the same internalName under a different tenant must
	// not surface another tenant's peers (separate cache key).
	if got := p.GetClusterPeers(internalName, "tenant-2"); got != nil {
		t.Fatalf("cross-tenant lookup leaked peers: %#v", got)
	}

	// Empty-identifier validation: both inputs are load-bearing in the key.
	if got := p.GetClusterPeers("", tenantID); got != nil {
		t.Fatalf("empty internalName should return nil, got %#v", got)
	}
	if got := p.GetClusterPeers(internalName, ""); got != nil {
		t.Fatalf("empty tenantID should return nil, got %#v", got)
	}
}

// TestStreamContextCacheSnapshot_ReflectsCachedEntries verifies the admin
// snapshot view materialises every streamContext entry with the correct
// projected fields and a Size matching the entry count. Non-streamContext
// values are skipped (the cache is otherwise shared), so the snapshot can't
// be corrupted by foreign value types.
func TestStreamContextCacheSnapshot_ReflectsCachedEntries(t *testing.T) {
	p := newTestProcessor(t)

	p.streamCache.Set("tenant-a:stream-1", streamContext{
		TenantID: "tenant-a",
		UserID:   "user-a",
		StreamID: "uuid-1",
		Source:   "commodore",
	}, time.Minute)
	p.streamCache.Set("tenant-b:stream-2", streamContext{
		TenantID: "tenant-b",
		StreamID: "uuid-2",
	}, time.Minute)
	// A foreign value type sharing the cache must be ignored, not panic.
	p.streamCache.Set("process:stream-3", "not-a-streamContext", time.Minute)

	snap := p.StreamContextCacheSnapshot()
	if snap.Size != len(snap.Entries) {
		t.Fatalf("Size %d != len(Entries) %d", snap.Size, len(snap.Entries))
	}
	if snap.Size != 2 {
		t.Fatalf("expected 2 streamContext entries (foreign type skipped), got %d", snap.Size)
	}

	byKey := map[string]StreamContextCacheEntry{}
	for _, e := range snap.Entries {
		byKey[e.Key] = e
	}
	a, ok := byKey["tenant-a:stream-1"]
	if !ok {
		t.Fatal("missing entry for tenant-a:stream-1")
	}
	if a.TenantID != "tenant-a" || a.UserID != "user-a" || a.StreamID != "uuid-1" || a.Source != "commodore" {
		t.Fatalf("entry projection wrong: %#v", a)
	}
	if _, ok := byKey["process:stream-3"]; ok {
		t.Fatal("foreign value type leaked into snapshot entries")
	}
}

// TestGetClientBatcher_LazyInitOnceUnderConcurrency locks the batcher
// lifecycle invariant: getClientBatcher constructs the CLIENT_LIFECYCLE
// batcher exactly once even when many goroutines race for it (deferred so the
// drop-counter metric is wired first). Two callers must get the same pointer.
func TestGetClientBatcher_LazyInitOnceUnderConcurrency(t *testing.T) {
	p := newTestProcessor(t)
	if p.clientBatcher != nil {
		t.Fatal("batcher should be nil before first getClientBatcher call")
	}

	const n = 16
	results := make([]*clientLifecycleBatcher, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = p.getClientBatcher()
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("getClientBatcher returned nil")
	}
	for i, b := range results {
		if b != first {
			t.Fatalf("concurrent call %d got a different batcher pointer; init raced", i)
		}
	}
	// Clean up the goroutine the batcher started.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
}

// TestShutdown_FlushesBatcherAndIsIdempotent verifies Shutdown drains the
// lazily-created batcher and that a second call is a safe no-op (the batcher's
// stopOnce guards the close), plus that Shutdown before any batcher exists
// returns nil without constructing one.
func TestShutdown_FlushesBatcherAndIsIdempotent(t *testing.T) {
	p := newTestProcessor(t)

	// No batcher yet: Shutdown is a clean no-op and must not lazily create one.
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown with no batcher returned %v", err)
	}
	if p.clientBatcher != nil {
		t.Fatal("Shutdown lazily created a batcher; it must stay nil")
	}

	// Force the batcher into existence, then shut it down twice.
	_ = p.getClientBatcher()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown failed: %v", err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown (idempotent) failed: %v", err)
	}
}

// TestProcessTypedTrigger_DispatchAndUnsupported pins the trigger-router
// invariant: each typed payload reaches its dedicated handler (verified via a
// handler-specific observable side effect / response), and an unrecognised
// payload returns the unsupported-payload error while echoing the trigger's
// Blocking flag back to Mist.
func TestProcessTypedTrigger_DispatchAndUnsupported(t *testing.T) {
	t.Run("nil trigger errors and aborts", func(t *testing.T) {
		p := newTestProcessor(t)
		_, abort, err := p.ProcessTypedTrigger(nil)
		if err == nil || !abort {
			t.Fatalf("nil trigger: err=%v abort=%v, want error+abort", err, abort)
		}
	})

	t.Run("RawMistWebhook routes to handleRawMistWebhook", func(t *testing.T) {
		// decklogClient is nil so the send fails, but RawMistWebhook is not in
		// the surface-error set: dispatch must reach the handler and swallow
		// the send error, returning ("", false, nil).
		p := newTestProcessor(t)
		tenantID := "tenant-1"
		resp, abort, err := p.ProcessTypedTrigger(&ipcpb.MistTrigger{
			TenantId:    &tenantID,
			TriggerType: "RAW",
			TriggerPayload: &ipcpb.MistTrigger_RawMistWebhook{
				RawMistWebhook: &ipcpb.RawMistWebhookTrigger{},
			},
		})
		if err != nil || abort || resp != "" {
			t.Fatalf("RawMistWebhook dispatch: resp=%q abort=%v err=%v", resp, abort, err)
		}
	})

	t.Run("StreamProcess vod+ routes to handleStreamProcess", func(t *testing.T) {
		// vod+ short-circuits to an empty config inside handleStreamProcess —
		// a unique observable that proves the StreamProcess arm dispatched.
		p := newTestProcessor(t)
		resp, abort, err := p.ProcessTypedTrigger(&ipcpb.MistTrigger{
			TriggerType: "STREAM_PROCESS",
			TriggerPayload: &ipcpb.MistTrigger_StreamProcess{
				StreamProcess: &ipcpb.StreamProcessTrigger{StreamName: "vod+asset-1"},
			},
		})
		if err != nil || abort || resp != "" {
			t.Fatalf("StreamProcess vod+ dispatch: resp=%q abort=%v err=%v", resp, abort, err)
		}
	})

	t.Run("unsupported payload errors and echoes Blocking", func(t *testing.T) {
		p := newTestProcessor(t)
		// A trigger with no payload set hits the default arm.
		_, abort, err := p.ProcessTypedTrigger(&ipcpb.MistTrigger{Blocking: true})
		if err == nil {
			t.Fatal("unsupported payload: expected error")
		}
		if !abort {
			t.Fatal("unsupported payload must echo Blocking=true back to Mist")
		}
	})
}

// TestHandleStreamProcess_PrefixSelectsConfig pins the process-config
// selection routing in handleStreamProcess:
//   - vod+ is the read-only playback path and must return an empty config
//     unconditionally (no Thumbs/sprite/Livepeer boot), even if a stale
//     process: cache entry exists.
//   - a live/processing/dvr stream with a process:<internal> cache entry
//     returns that cached config verbatim (the live-ingest fast path).
//   - a nil streamCache short-circuits to empty config.
func TestHandleStreamProcess_PrefixSelectsConfig(t *testing.T) {
	t.Run("vod+ returns empty even with a cached process config", func(t *testing.T) {
		p := newTestProcessor(t)
		// Stale cache entry that must NOT be served for vod+.
		p.streamCache.Set("process:asset-1", `{"PROC":"stale"}`, time.Minute)
		resp, abort, err := p.handleStreamProcess(&ipcpb.MistTrigger{
			TriggerPayload: &ipcpb.MistTrigger_StreamProcess{
				StreamProcess: &ipcpb.StreamProcessTrigger{StreamName: "vod+asset-1"},
			},
		})
		if err != nil || abort || resp != "" {
			t.Fatalf("vod+ should return empty config: resp=%q abort=%v err=%v", resp, abort, err)
		}
	})

	t.Run("cached process config is served for non-vod streams", func(t *testing.T) {
		p := newTestProcessor(t)
		cfg := `{"PROC":"live-config"}`
		p.streamCache.Set("process:live-stream-1", cfg, time.Minute)
		resp, abort, err := p.handleStreamProcess(&ipcpb.MistTrigger{
			TriggerPayload: &ipcpb.MistTrigger_StreamProcess{
				StreamProcess: &ipcpb.StreamProcessTrigger{StreamName: "live+live-stream-1"},
			},
		})
		if err != nil || abort {
			t.Fatalf("cached path err=%v abort=%v", err, abort)
		}
		if resp != cfg {
			t.Fatalf("cached process config = %q, want %q", resp, cfg)
		}
	})

	t.Run("nil streamCache returns empty config", func(t *testing.T) {
		// processing+/dvr+ DB fallbacks live behind control.GetDB(); with no
		// cache and no DB the handler must safely return empty rather than
		// dereferencing a nil cache.
		p := &Processor{logger: newTestProcessor(t).logger}
		resp, abort, err := p.handleStreamProcess(&ipcpb.MistTrigger{
			TriggerPayload: &ipcpb.MistTrigger_StreamProcess{
				StreamProcess: &ipcpb.StreamProcessTrigger{StreamName: "processing+hash-1"},
			},
		})
		if err != nil || abort || resp != "" {
			t.Fatalf("nil cache: resp=%q abort=%v err=%v", resp, abort, err)
		}
	})
}

// TestHandleRawMistWebhook_ErrorSurfacing pins the error-surfacing contract of
// the raw-webhook forwarder. The trigger is forwarded to Decklog; a send
// failure is surfaced to Mist (aborts the trigger) ONLY when the trigger type
// is in the surface set (USER_END etc.), otherwise it is swallowed so a
// fire-and-forget webhook can't wedge the Mist control path.
func TestHandleRawMistWebhook_ErrorSurfacing(t *testing.T) {
	// decklogClient is nil in newTestProcessor → sendTriggerToDecklog always
	// fails once a tenant_id is present.
	t.Run("non-surface type swallows send error", func(t *testing.T) {
		p := newTestProcessor(t)
		tenantID := "tenant-1"
		resp, abort, err := p.handleRawMistWebhook(&ipcpb.MistTrigger{
			TenantId:    &tenantID,
			TriggerType: "DEFAULT_STREAM",
			TriggerPayload: &ipcpb.MistTrigger_RawMistWebhook{
				RawMistWebhook: &ipcpb.RawMistWebhookTrigger{},
			},
		})
		if err != nil || abort || resp != "" {
			t.Fatalf("non-surface webhook: resp=%q abort=%v err=%v", resp, abort, err)
		}
	})

	t.Run("surface type propagates send error", func(t *testing.T) {
		p := newTestProcessor(t)
		tenantID := "tenant-1"
		_, _, err := p.handleRawMistWebhook(&ipcpb.MistTrigger{
			TenantId:    &tenantID,
			TriggerType: "USER_END", // in shouldSurfaceDecklogError's surface set
			TriggerPayload: &ipcpb.MistTrigger_RawMistWebhook{
				RawMistWebhook: &ipcpb.RawMistWebhookTrigger{},
			},
		})
		if err == nil {
			t.Fatal("surface-set webhook should propagate the Decklog send error")
		}
	})

	t.Run("wrong payload type errors", func(t *testing.T) {
		p := newTestProcessor(t)
		_, _, err := p.handleRawMistWebhook(&ipcpb.MistTrigger{
			TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
				ProcessBilling: &ipcpb.ProcessBillingEvent{},
			},
		})
		if err == nil {
			t.Fatal("wrong payload type should error")
		}
	})
}
