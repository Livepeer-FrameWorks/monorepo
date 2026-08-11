package triggers

import (
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newDecklogSendCounter returns a fresh DecklogTriggerSends-shaped counter. sendTriggerToDecklog increments
// {trigger_type, "attempt"} the instant it is REACHED (before the client-nil check), so a zero "attempt" count
// proves the handler dropped/suppressed the forward BEFORE attempting a send.
func newDecklogSendCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_decklog_sends"}, []string{"trigger_type", "status"})
}

// The node-originated lifecycle families attribute an event to a tenant. When a node ASSERTS a tenant but
// the resource owner cannot be resolved, the assertion is unverifiable (the conflict check has nothing
// authoritative to contradict). The pure-analytics families (storage / DVR / client QoE) must DROP such an
// event before forwarding — never letting an unverified node claim pollute another tenant's rollup — matching
// process_billing's fail-closed-on-unresolvable stance. streamCache is empty in newTestProcessor, so every
// resource key below is unresolvable.
func TestLifecycleFamiliesDropUnverifiableAssertedTenant(t *testing.T) {
	const asserted = "tenant-forged"

	t.Run("storage lifecycle dropped when owner unresolved", func(t *testing.T) {
		p := newTestProcessor(t)
		c := newDecklogSendCounter()
		p.metrics = &ProcessorMetrics{DecklogTriggerSends: c}
		tenant := asserted
		sld := &ipcpb.StorageLifecycleData{TenantId: &tenant, AssetHash: "unresolvable-hash"}
		trigger := &ipcpb.MistTrigger{TriggerType: "STORAGE_LIFECYCLE_DATA", TriggerPayload: &ipcpb.MistTrigger_StorageLifecycleData{StorageLifecycleData: sld}}
		if _, _, err := p.handleStorageLifecycleData(trigger); err != nil {
			t.Fatalf("handler err: %v", err)
		}
		if v := testutil.ToFloat64(c.WithLabelValues("STORAGE_LIFECYCLE_DATA", "attempt")); v != 0 {
			t.Fatalf("unverifiable storage lifecycle must be dropped before forward; attempt=%v", v)
		}
	})

	t.Run("DVR lifecycle dropped when owner unresolved", func(t *testing.T) {
		p := newTestProcessor(t)
		c := newDecklogSendCounter()
		p.metrics = &ProcessorMetrics{DecklogTriggerSends: c}
		tenant := asserted
		dld := &ipcpb.DVRLifecycleData{TenantId: &tenant, DvrHash: "unresolvable-hash"}
		trigger := &ipcpb.MistTrigger{TriggerType: "DVR_LIFECYCLE_DATA", TriggerPayload: &ipcpb.MistTrigger_DvrLifecycleData{DvrLifecycleData: dld}}
		if _, _, err := p.handleDVRLifecycleData(trigger); err != nil {
			t.Fatalf("handler err: %v", err)
		}
		if v := testutil.ToFloat64(c.WithLabelValues("DVR_LIFECYCLE_DATA", "attempt")); v != 0 {
			t.Fatalf("unverifiable DVR lifecycle must be dropped before forward; attempt=%v", v)
		}
	})

	t.Run("client lifecycle QoE dropped when owner unresolved", func(t *testing.T) {
		p := newTestProcessor(t)
		c := newDecklogSendCounter()
		p.metrics = &ProcessorMetrics{DecklogTriggerSends: c}
		tenant := asserted
		clu := &ipcpb.ClientLifecycleUpdate{TenantId: &tenant, InternalName: "unresolvable-stream"}
		trigger := &ipcpb.MistTrigger{TriggerType: "CLIENT_LIFECYCLE_UPDATE", TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleUpdate{ClientLifecycleUpdate: clu}}
		if _, _, err := p.handleClientLifecycleUpdate(trigger); err != nil {
			t.Fatalf("handler err: %v", err)
		}
		if v := testutil.ToFloat64(c.WithLabelValues("CLIENT_LIFECYCLE_UPDATE", "attempt")); v != 0 {
			t.Fatalf("unverifiable client lifecycle must be dropped before forward; attempt=%v", v)
		}
	})
}

func TestAssertedTenantConflicts(t *testing.T) {
	cases := []struct {
		name     string
		asserted string
		resolved string
		want     bool
	}{
		{"both empty", "", "", false},
		{"asserted only (unresolvable → cannot verify)", "tenant-a", "", false},
		{"resolved only (nothing asserted)", "", "tenant-a", false},
		{"match", "tenant-a", "tenant-a", false},
		{"whitespace match", " tenant-a ", "tenant-a", false},
		{"conflict", "tenant-b", "tenant-a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assertedTenantConflicts(tc.asserted, tc.resolved); got != tc.want {
				t.Fatalf("assertedTenantConflicts(%q, %q) = %v, want %v", tc.asserted, tc.resolved, got, tc.want)
			}
		})
	}
}

// The server-resolved resource owner is authoritative: a node-asserted tenant that disagrees is overwritten
// with the resolved owner (the forged value never propagates), while a matching assertion is preserved.
func TestApplyStreamContext_ServerResolvedOwnerOverridesAssertion(t *testing.T) {
	t.Run("conflicting assertion is overwritten by resolved owner", func(t *testing.T) {
		p := newTestProcessor(t)
		// applyStreamContext passes the trigger's asserted tenant as the resolver cache hint, so the key is
		// "<asserted>:<internalName>". Seed the resolver's view: the resource truly belongs to tenant-a.
		p.streamCache.Set("tenant-b:stream-x", streamContext{TenantID: "tenant-a", StreamID: "sid-1"}, time.Minute)

		asserted := "tenant-b"
		trigger := &ipcpb.MistTrigger{TriggerType: "STREAM_LIFECYCLE", TenantId: &asserted}
		info := p.applyStreamContext(trigger, "stream-x")

		if info.TenantID != "tenant-a" {
			t.Fatalf("resolver owns the resource as tenant-a, got %q", info.TenantID)
		}
		if trigger.GetTenantId() != "tenant-a" {
			t.Fatalf("conflicting asserted tenant must be overwritten by the resolved owner; got %q", trigger.GetTenantId())
		}
	})

	t.Run("matching assertion is preserved", func(t *testing.T) {
		p := newTestProcessor(t)
		p.streamCache.Set("tenant-a:stream-y", streamContext{TenantID: "tenant-a"}, time.Minute)

		asserted := "tenant-a"
		trigger := &ipcpb.MistTrigger{TriggerType: "STREAM_LIFECYCLE", TenantId: &asserted}
		p.applyStreamContext(trigger, "stream-y")

		if trigger.GetTenantId() != "tenant-a" {
			t.Fatalf("matching assertion must be kept; got %q", trigger.GetTenantId())
		}
	})
}
