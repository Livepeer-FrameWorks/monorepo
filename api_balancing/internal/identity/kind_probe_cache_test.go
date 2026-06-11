package identity

import (
	"context"
	"errors"
	"testing"
)

// Per-kind probe outcomes are negative-cached in their own keyspace ("ak:")
// so partial knowledge survives a mixed round: a kind that authoritatively
// missed is not re-probed while other kinds' transient errors are retried.
// The whole-call "a:" negative key keeps its strict authoritative-and-not-
// transient contract untouched.

func TestResolveArtifact_PerKindProbePartialCaching(t *testing.T) {
	var probedKinds []string
	r := NewResolver(Config{
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			probedKinds = append(probedKinds, kind)
			if kind == "clip" {
				return ArtifactIdentity{}, ErrNotFound // authoritative per-kind miss
			}
			return ArtifactIdentity{}, errors.New("rpc down") // transient
		},
	})

	if _, err := r.ResolveArtifact(context.Background(), "hash-1", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	if len(probedKinds) != 3 {
		t.Fatalf("first call probed %v, want all three kinds", probedKinds)
	}

	// Second call: clip's authoritative miss is cached; vod/dvr (transient)
	// are re-probed. The whole-call key was NOT stored (transient present).
	probedKinds = nil
	if _, err := r.ResolveArtifact(context.Background(), "hash-1", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	if len(probedKinds) != 2 || probedKinds[0] != "vod" || probedKinds[1] != "dvr" {
		t.Fatalf("second call probed %v, want only [vod dvr]", probedKinds)
	}
}

func TestResolveArtifact_HintedMissSeedsKindCache(t *testing.T) {
	var probedKinds []string
	r := NewResolver(Config{
		// Registry layer present and transient-erroring: keeps the whole-call
		// negative key from being stored, isolating the per-kind cache.
		RegistryArtifact: func(ctx context.Context, hash string) (ArtifactIdentity, error) {
			return ArtifactIdentity{}, errors.New("registry down")
		},
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			probedKinds = append(probedKinds, kind)
			return ArtifactIdentity{}, ErrNotFound
		},
	})

	// Hinted clip miss seeds the per-kind cache...
	if _, err := r.ResolveArtifact(context.Background(), "hash-1", "clip"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	// ...so the hintless call probes only vod+dvr.
	probedKinds = nil
	if _, err := r.ResolveArtifact(context.Background(), "hash-1", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	if len(probedKinds) != 2 || probedKinds[0] != "vod" || probedKinds[1] != "dvr" {
		t.Fatalf("hintless call probed %v, want only [vod dvr]", probedKinds)
	}
}

func TestResolveArtifact_TransientProbeNotKindCached(t *testing.T) {
	var probedKinds []string
	r := NewResolver(Config{
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			probedKinds = append(probedKinds, kind)
			return ArtifactIdentity{}, errors.New("rpc down")
		},
	})

	for range 2 {
		probedKinds = nil
		if _, err := r.ResolveArtifact(context.Background(), "hash-1", ""); !errors.Is(err, ErrUnknown) {
			t.Fatalf("err = %v, want ErrUnknown", err)
		}
		if len(probedKinds) != 3 {
			t.Fatalf("probed %v, want all three kinds every call (transient never cached)", probedKinds)
		}
	}
}

// A kind that previously missed authoritatively must not block a LATER hit on
// a different kind: the cached clip miss skips one probe, vod still resolves.
func TestResolveArtifact_KindCacheDoesNotBlockOtherKinds(t *testing.T) {
	vodProbes := 0
	r := NewResolver(Config{
		CommodoreArtifact: func(ctx context.Context, kind, hash string) (ArtifactIdentity, error) {
			switch kind {
			case "clip":
				return ArtifactIdentity{}, ErrNotFound
			case "vod":
				vodProbes++
				if vodProbes == 1 {
					return ArtifactIdentity{}, errors.New("rpc down")
				}
				return ArtifactIdentity{TenantID: "tenant-1", InternalName: "vodintl"}, nil
			default:
				return ArtifactIdentity{}, ErrNotFound
			}
		},
	})

	// First call: clip authoritative miss cached, vod transient.
	if _, err := r.ResolveArtifact(context.Background(), "hash-1", ""); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	// Second call: clip skipped from cache, vod now resolves.
	id, err := r.ResolveArtifact(context.Background(), "hash-1", "")
	if err != nil {
		t.Fatalf("err = %v, want vod hit", err)
	}
	if id.Kind != "vod" || id.TenantID != "tenant-1" {
		t.Fatalf("resolved identity = %+v, want vod/tenant-1", id)
	}
}
