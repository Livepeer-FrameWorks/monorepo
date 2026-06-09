package loaders

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestStreamLoader_LoadCachesAndAvoidsSecondBackendCall(t *testing.T) {
	fake := &clientstest.FakeCommodore{
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id}, nil
		},
	}
	l := NewStreamLoader(fake)
	ctx := context.Background()

	for range 3 {
		s, err := l.Load(ctx, "t1", "s1")
		if err != nil || s.StreamId != "s1" {
			t.Fatalf("Load → (%v,%v)", s, err)
		}
	}
	if fake.Calls != 1 {
		t.Fatalf("backend called %d times, want 1 (cached)", fake.Calls)
	}
}

func TestStreamLoader_TenantKeyIsolation(t *testing.T) {
	// Same streamID under two tenants must NOT collide in the cache.
	fake := &clientstest.FakeCommodore{
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id}, nil
		},
	}
	l := NewStreamLoader(fake)
	ctx := context.Background()
	_, _ = l.Load(ctx, "tenantA", "s1")
	_, _ = l.Load(ctx, "tenantB", "s1")
	if fake.Calls != 2 {
		t.Fatalf("backend called %d times, want 2 (distinct tenant keys)", fake.Calls)
	}
}

func TestStreamLoader_LoadManyDedupesAndKeysByInput(t *testing.T) {
	var requested []string
	fake := &clientstest.FakeCommodore{
		GetStreamsBatchFn: func(_ context.Context, ids []string) (*commodorepb.GetStreamsBatchResponse, error) {
			requested = ids
			streams := make([]*commodorepb.Stream, 0, len(ids))
			for _, id := range ids {
				if id == "missing" {
					continue // simulate not-found: absent from response
				}
				streams = append(streams, &commodorepb.Stream{StreamId: id})
			}
			return &commodorepb.GetStreamsBatchResponse{Streams: streams}, nil
		},
	}
	l := NewStreamLoader(fake)

	res, err := l.LoadMany(context.Background(), "t1", []string{"s1", "s2", "s1", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	// LoadMany dedupes against the cache, not within a single batch — uncached
	// ids pass through verbatim (duplicate "s1" included). Callers that need
	// in-batch dedup go through PreloadStreams (covered separately).
	if len(requested) != 4 {
		t.Fatalf("batch requested %v, want all 4 uncached ids passed through", requested)
	}
	// The result map still collapses duplicates by key.
	if res["s1"] == nil || res["s2"] == nil {
		t.Fatalf("expected s1 and s2 present, got %v", res)
	}
	// Not-found id is keyed to a nil value (so callers can distinguish).
	if v, ok := res["missing"]; !ok || v != nil {
		t.Fatalf("missing should map to nil, got ok=%v v=%v", ok, v)
	}

	// A follow-up LoadMany for the same ids is fully cache-served (incl. the nil).
	fake.Calls = 0
	if _, err := l.LoadMany(context.Background(), "t1", []string{"s1", "s2", "missing"}); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 0 {
		t.Fatalf("second LoadMany hit backend %d times, want 0", fake.Calls)
	}
}

func TestStreamLoader_PrimeServesWithoutBackend(t *testing.T) {
	// No GetStreamFn set → any backend call panics; Prime must satisfy reads.
	fake := &clientstest.FakeCommodore{}
	l := NewStreamLoader(fake)
	l.Prime("t1", &commodorepb.Stream{StreamId: "s1"})
	l.PrimeMany("t1", []*commodorepb.Stream{{StreamId: "s2"}, nil, {StreamId: ""}})

	for _, id := range []string{"s1", "s2"} {
		s, err := l.Load(context.Background(), "t1", id)
		if err != nil || s == nil || s.StreamId != id {
			t.Fatalf("Load(%s) → (%v,%v)", id, s, err)
		}
	}
	if fake.Calls != 0 {
		t.Fatalf("primed reads hit backend %d times, want 0", fake.Calls)
	}
}

func TestStreamLoader_PrimeNilSuppressesRetry(t *testing.T) {
	fake := &clientstest.FakeCommodore{}
	l := NewStreamLoader(fake)
	l.PrimeNil("t1", []string{"gone", ""})

	s, err := l.Load(context.Background(), "t1", "gone")
	if err != nil || s != nil {
		t.Fatalf("Load(gone) → (%v,%v), want (nil,nil) from primed-nil", s, err)
	}
	if fake.Calls != 0 {
		t.Fatalf("primed-nil read hit backend %d times, want 0", fake.Calls)
	}
}

func TestStreamLoader_LoadPropagatesError(t *testing.T) {
	sentinel := errors.New("commodore down")
	fake := &clientstest.FakeCommodore{
		GetStreamFn: func(context.Context, string) (*commodorepb.Stream, error) { return nil, sentinel },
	}
	l := NewStreamLoader(fake)
	if _, err := l.Load(context.Background(), "t1", "s1"); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
}

func TestPreloadStreams_BatchesIntoContextLoader(t *testing.T) {
	fake := &clientstest.FakeCommodore{
		GetStreamsBatchFn: func(_ context.Context, ids []string) (*commodorepb.GetStreamsBatchResponse, error) {
			streams := make([]*commodorepb.Stream, len(ids))
			for i, id := range ids {
				streams[i] = &commodorepb.Stream{StreamId: id}
			}
			return &commodorepb.GetStreamsBatchResponse{Streams: streams}, nil
		},
	}
	l := &Loaders{Stream: NewStreamLoader(fake)}
	ctx := ContextWithLoaders(context.Background(), l)

	// Duplicate + empty ids are filtered before the batch call.
	PreloadStreams(ctx, "t1", []string{"s1", "s1", "", "s2"})
	if fake.Calls != 1 {
		t.Fatalf("backend called %d times, want 1", fake.Calls)
	}
	// Preloaded streams are now cache-served.
	s, err := l.Stream.Load(ctx, "t1", "s1")
	if err != nil || s.StreamId != "s1" {
		t.Fatalf("post-preload Load → (%v,%v)", s, err)
	}

	// No loaders in context → no-op, no panic.
	PreloadStreams(context.Background(), "t1", []string{"s1"})
}
