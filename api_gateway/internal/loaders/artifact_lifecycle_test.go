package loaders

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

func TestArtifactLifecycle_LoadCachesIncludingMisses(t *testing.T) {
	fake := &clientstest.FakePeriscope{
		GetArtifactStatesByIDsFn: func(_ context.Context, _ string, ids []string, _ *string) (*periscopepb.GetArtifactStatesResponse, error) {
			// Return nothing → simulate a missing artifact.
			return &periscopepb.GetArtifactStatesResponse{}, nil
		},
	}
	l := NewArtifactLifecycleLoader(fake)
	ctx := context.Background()

	got, err := l.Load(ctx, "t1", "r1")
	if err != nil || got != nil {
		t.Fatalf("Load → (%v,%v), want (nil,nil)", got, err)
	}
	// Second Load must be served from the cached nil — no second backend call.
	if _, err := l.Load(ctx, "t1", "r1"); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 1 {
		t.Fatalf("backend called %d times, want 1 (missing result cached)", fake.Calls)
	}
}

func TestArtifactLifecycle_LoadManyDedupAndOrder(t *testing.T) {
	var requested []string
	fake := &clientstest.FakePeriscope{
		GetArtifactStatesByIDsFn: func(_ context.Context, _ string, ids []string, _ *string) (*periscopepb.GetArtifactStatesResponse, error) {
			requested = ids
			out := make([]*periscopepb.ArtifactState, 0, len(ids))
			for _, id := range ids {
				if id == "missing" {
					continue
				}
				out = append(out, &periscopepb.ArtifactState{RequestId: id})
			}
			return &periscopepb.GetArtifactStatesResponse{Artifacts: out}, nil
		},
	}
	l := NewArtifactLifecycleLoader(fake)

	res, err := l.LoadMany(context.Background(), "t1", []string{"r1", "r2", "r1", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	// Dedup is against the cache, not within the batch: all 4 uncached ids
	// (including the duplicate r1) are forwarded.
	if len(requested) != 4 {
		t.Fatalf("batch requested %v, want all 4 forwarded", requested)
	}
	if res["r1"] == nil || res["r2"] == nil {
		t.Fatalf("expected r1,r2 present: %v", res)
	}
	if v, ok := res["missing"]; !ok || v != nil {
		t.Fatalf("missing should be cached nil, got ok=%v v=%v", ok, v)
	}

	fake.Calls = 0
	if _, err := l.LoadMany(context.Background(), "t1", []string{"r1", "missing"}); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 0 {
		t.Fatalf("second LoadMany hit backend %d times, want 0", fake.Calls)
	}
}

func TestArtifactLifecycle_PrimeAndPrimeNil(t *testing.T) {
	fake := &clientstest.FakePeriscope{}
	l := NewArtifactLifecycleLoader(fake)
	l.Prime("t1", &periscopepb.ArtifactState{RequestId: "r1"})
	l.PrimeMany("t1", []*periscopepb.ArtifactState{{RequestId: "r2"}, nil, {RequestId: ""}})
	l.PrimeNil("t1", []string{"gone", ""})

	if s, err := l.Load(context.Background(), "t1", "r1"); err != nil || s == nil {
		t.Fatalf("primed r1 → (%v,%v)", s, err)
	}
	if s, err := l.Load(context.Background(), "t1", "gone"); err != nil || s != nil {
		t.Fatalf("primed-nil gone → (%v,%v)", s, err)
	}
	if fake.Calls != 0 {
		t.Fatalf("primed reads hit backend %d times, want 0", fake.Calls)
	}
}

func TestArtifactLifecycle_LoadManyPropagatesError(t *testing.T) {
	sentinel := errors.New("periscope down")
	fake := &clientstest.FakePeriscope{
		GetArtifactStatesByIDsFn: func(context.Context, string, []string, *string) (*periscopepb.GetArtifactStatesResponse, error) {
			return nil, sentinel
		},
	}
	l := NewArtifactLifecycleLoader(fake)
	if _, err := l.LoadMany(context.Background(), "t1", []string{"r1"}); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
}
