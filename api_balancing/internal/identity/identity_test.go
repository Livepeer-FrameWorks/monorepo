package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveStream_StatePlusRegistryMerge(t *testing.T) {
	registryCalls := 0
	r := NewResolver(Config{
		StreamState: func(name string) (StreamStateView, bool) {
			// State knows node + tenant but not stream UUID or origin.
			return StreamStateView{TenantID: "tenant-1", NodeID: "node-1"}, true
		},
		NodeCluster: func(nodeID string) string {
			if nodeID == "node-1" {
				return "cluster-a"
			}
			return ""
		},
		RegistrySource: func(ctx context.Context, name string) (StreamIdentity, error) {
			registryCalls++
			return StreamIdentity{StreamID: "sid-1", OriginClusterID: "cluster-origin", TenantID: "tenant-1"}, nil
		},
	})

	id, err := r.ResolveStream(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if id.TenantID != "tenant-1" || id.NodeID != "node-1" || id.ServingCluster != "cluster-a" {
		t.Fatalf("state fields wrong: %+v", id)
	}
	if id.StreamID != "sid-1" || id.OriginClusterID != "cluster-origin" {
		t.Fatalf("registry fields not merged: %+v", id)
	}
	if id.Source != "state" {
		t.Fatalf("tenant was attributed by state, got Source=%q", id.Source)
	}
	if registryCalls != 1 {
		t.Fatalf("registry consulted %d times", registryCalls)
	}
}

func TestResolveStream_RegistryNeverErasesState(t *testing.T) {
	r := NewResolver(Config{
		StreamState: func(name string) (StreamStateView, bool) {
			return StreamStateView{TenantID: "tenant-1", StreamID: "sid-1"}, true
		},
		RegistrySource: func(ctx context.Context, name string) (StreamIdentity, error) {
			// Registry hit with EMPTY identity must not clobber.
			return StreamIdentity{OriginClusterID: "cluster-origin"}, nil
		},
	})
	id, err := r.ResolveStream(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if id.TenantID != "tenant-1" || id.StreamID != "sid-1" {
		t.Fatalf("registry empty fields erased state identity: %+v", id)
	}
}

func TestResolveStream_AuthoritativeMissNegativeCached(t *testing.T) {
	registryCalls := 0
	r := NewResolver(Config{
		StreamState: func(name string) (StreamStateView, bool) { return StreamStateView{}, false },
		RegistrySource: func(ctx context.Context, name string) (StreamIdentity, error) {
			registryCalls++
			return StreamIdentity{}, ErrNotFound
		},
		NegativeTTL: time.Minute,
	})

	for range 5 {
		if _, err := r.ResolveStream(context.Background(), "ghost"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("expected ErrUnknown, got %v", err)
		}
	}
	if registryCalls != 1 {
		t.Fatalf("negative cache did not suppress repeat lookups: %d registry calls", registryCalls)
	}
}

func TestResolveStream_TransientRegistryErrorNotCached(t *testing.T) {
	// A Commodore/DB outage surfaces as a generic error: every call must
	// retry instead of hardening into a 30s ErrUnknown.
	registryCalls := 0
	r := NewResolver(Config{
		RegistrySource: func(ctx context.Context, name string) (StreamIdentity, error) {
			registryCalls++
			return StreamIdentity{}, errors.New("commodore lookup: connection refused")
		},
		NegativeTTL: time.Minute,
	})

	for range 3 {
		if _, err := r.ResolveStream(context.Background(), "abc"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("expected ErrUnknown, got %v", err)
		}
	}
	if registryCalls != 3 {
		t.Fatalf("transient registry failure was negative-cached: %d registry calls", registryCalls)
	}
}

func TestResolveStream_StateOnlyMissNotCached(t *testing.T) {
	// With no authoritative layer wired, a state miss is not cached:
	// state can change on the very next trigger (PUSH_REWRITE).
	stateCalls := 0
	r := NewResolver(Config{
		StreamState: func(name string) (StreamStateView, bool) {
			stateCalls++
			return StreamStateView{}, false
		},
		NegativeTTL: time.Minute,
	})
	for range 3 {
		if _, err := r.ResolveStream(context.Background(), "abc"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("expected ErrUnknown, got %v", err)
		}
	}
	if stateCalls != 3 {
		t.Fatalf("state-only miss was negative-cached: %d state calls", stateCalls)
	}
}

func TestResolveStream_ContextErrorNotNegativeCached(t *testing.T) {
	registryCalls := 0
	r := NewResolver(Config{
		RegistrySource: func(ctx context.Context, name string) (StreamIdentity, error) {
			registryCalls++
			return StreamIdentity{}, ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ResolveStream(ctx, "abc"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected ErrUnknown, got %v", err)
	}
	// A fresh, healthy context must retry — the failure was transient.
	if _, err := r.ResolveStream(context.Background(), "abc"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected ErrUnknown, got %v", err)
	}
	if registryCalls != 2 {
		t.Fatalf("transient failure was negative-cached: %d registry calls", registryCalls)
	}
}

func TestResolveArtifact_RegistryThenCommodoreFallback(t *testing.T) {
	var probedKinds []string
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{}, ErrNotFound
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			probedKinds = append(probedKinds, kind)
			if kind == "vod" {
				return ArtifactIdentity{TenantID: "tenant-1", StreamInternalName: "src", OriginClusterID: "cluster-o"}, nil
			}
			return ArtifactIdentity{}, nil
		},
	})

	id, err := r.ResolveArtifact(context.Background(), "hash-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if id.TenantID != "tenant-1" || id.Kind != "vod" || id.Source != "commodore" {
		t.Fatalf("commodore fallback wrong: %+v", id)
	}
	if len(probedKinds) != 2 || probedKinds[0] != "clip" || probedKinds[1] != "vod" {
		t.Fatalf("probe order wrong: %v", probedKinds)
	}
}

func TestResolveArtifact_KindHintPinsSingleProbe(t *testing.T) {
	var probedKinds []string
	r := NewResolver(Config{
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			probedKinds = append(probedKinds, kind)
			return ArtifactIdentity{TenantID: "tenant-1"}, nil
		},
	})
	id, err := r.ResolveArtifact(context.Background(), "hash-1", "dvr")
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != "dvr" || len(probedKinds) != 1 || probedKinds[0] != "dvr" {
		t.Fatalf("kind hint not honored: %+v probes=%v", id, probedKinds)
	}
}

func TestResolveArtifact_CompleteRegistryHitSkipsCommodore(t *testing.T) {
	commodoreCalls := 0
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{Kind: "clip", TenantID: "tenant-1", StreamInternalName: "src", StorageClusterID: "cluster-s"}, nil
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			commodoreCalls++
			return ArtifactIdentity{}, nil
		},
	})
	id, err := r.ResolveArtifact(context.Background(), "hash-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if id.Source != "registry" || id.StorageClusterID != "cluster-s" {
		t.Fatalf("registry attribution wrong: %+v", id)
	}
	if commodoreCalls != 0 {
		t.Fatal("commodore consulted despite complete registry answer")
	}
}

func TestResolveArtifact_CommodoreFillsMissingStreamName(t *testing.T) {
	// A healed local row carries the tenant but not the parent stream
	// name clip S3 keys embed; Commodore fills the blank without
	// overwriting what the registry attributed.
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{Kind: "clip", TenantID: "tenant-1"}, nil
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{TenantID: "tenant-1", StreamInternalName: "src", OriginClusterID: "cluster-o"}, nil
		},
	})
	id, err := r.ResolveArtifact(context.Background(), "hash-1", "clip")
	if err != nil {
		t.Fatal(err)
	}
	if id.StreamInternalName != "src" || id.OriginClusterID != "cluster-o" {
		t.Fatalf("commodore did not fill blanks: %+v", id)
	}
	if id.Source != "registry" {
		t.Fatalf("tenant was attributed by registry, got Source=%q", id.Source)
	}
}

func TestResolveArtifact_CrossTenantProbeRejected(t *testing.T) {
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{Kind: "clip", TenantID: "tenant-1"}, nil
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{TenantID: "tenant-OTHER", StreamInternalName: "evil"}, nil
		},
	})
	id, err := r.ResolveArtifact(context.Background(), "hash-1", "clip")
	if err != nil {
		t.Fatal(err)
	}
	if id.StreamInternalName != "" {
		t.Fatalf("cross-tenant probe result was merged: %+v", id)
	}
}

func TestResolveArtifact_AuthoritativeMissNegativeCached(t *testing.T) {
	registryCalls, commodoreCalls := 0, 0
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			registryCalls++
			return ArtifactIdentity{}, ErrNotFound
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			commodoreCalls++
			return ArtifactIdentity{}, nil // adapters' "found nothing" answer
		},
		NegativeTTL: time.Minute,
	})
	for range 4 {
		if _, err := r.ResolveArtifact(context.Background(), "ghost", "clip"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("expected ErrUnknown, got %v", err)
		}
	}
	if registryCalls != 1 || commodoreCalls != 1 {
		t.Fatalf("authoritative miss not cached: registry=%d commodore=%d", registryCalls, commodoreCalls)
	}
}

func TestResolveArtifact_TransientErrorsNotCached(t *testing.T) {
	registryCalls, commodoreCalls := 0, 0
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			registryCalls++
			return ArtifactIdentity{}, errors.New("sql: database is closed")
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			commodoreCalls++
			return ArtifactIdentity{}, errors.New("rpc error: unavailable")
		},
		NegativeTTL: time.Minute,
	})
	for range 3 {
		if _, err := r.ResolveArtifact(context.Background(), "hash-1", "clip"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("expected ErrUnknown, got %v", err)
		}
	}
	if registryCalls != 3 || commodoreCalls != 3 {
		t.Fatalf("transient failures were negative-cached: registry=%d commodore=%d", registryCalls, commodoreCalls)
	}
}

func TestResolveArtifact_MixedAuthoritativeAndTransientNotCached(t *testing.T) {
	// Registry says not-found, but the Commodore probe fails transiently:
	// Commodore might still know the hash, so the miss must not harden.
	commodoreCalls := 0
	r := NewResolver(Config{
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{}, ErrNotFound
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			commodoreCalls++
			return ArtifactIdentity{}, errors.New("rpc error: unavailable")
		},
		NegativeTTL: time.Minute,
	})
	for range 3 {
		if _, err := r.ResolveArtifact(context.Background(), "hash-1", "clip"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("expected ErrUnknown, got %v", err)
		}
	}
	if commodoreCalls != 3 {
		t.Fatalf("mixed transient outcome was negative-cached: commodore=%d", commodoreCalls)
	}
}

func TestResolveStream_NilLayersFailClosed(t *testing.T) {
	r := NewResolver(Config{})
	if _, err := r.ResolveStream(context.Background(), "abc"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected ErrUnknown with no layers, got %v", err)
	}
	if _, err := r.ResolveArtifact(context.Background(), "hash", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected ErrUnknown with no layers, got %v", err)
	}
}

func TestObserveHook(t *testing.T) {
	type obs struct{ kind, layer, outcome string }
	var seen []obs
	r := NewResolver(Config{
		StreamState: func(name string) (StreamStateView, bool) {
			return StreamStateView{TenantID: "tenant-1", StreamID: "sid"}, true
		},
		RegistrySource: func(ctx context.Context, name string) (StreamIdentity, error) {
			return StreamIdentity{OriginClusterID: "c"}, nil
		},
		Observe: func(kind, layer, outcome string) { seen = append(seen, obs{kind, layer, outcome}) },
	})
	if _, err := r.ResolveStream(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != (obs{"stream", "state", "hit"}) || seen[1] != (obs{"stream", "registry", "hit"}) {
		t.Fatalf("observations wrong: %v", seen)
	}
}
