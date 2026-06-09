package loaders

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

func TestStreamMetrics_LoadCaches(t *testing.T) {
	fake := &clientstest.FakePeriscope{
		GetStreamStatusFn: func(_ context.Context, _, name string) (*periscopepb.StreamStatusResponse, error) {
			return &periscopepb.StreamStatusResponse{}, nil
		},
	}
	l := NewStreamMetricsLoader(fake)
	ctx := context.Background()
	for range 3 {
		if _, err := l.Load(ctx, "t1", "live+s1"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.Calls != 1 {
		t.Fatalf("backend called %d times, want 1", fake.Calls)
	}
}

func TestStreamMetrics_LoadManyDedupAndCacheServe(t *testing.T) {
	var requested []string
	fake := &clientstest.FakePeriscope{
		GetStreamsStatusFn: func(_ context.Context, _ string, names []string) (*periscopepb.StreamsStatusResponse, error) {
			requested = names
			statuses := map[string]*periscopepb.StreamStatusResponse{}
			for _, n := range names {
				if n == "absent" {
					continue // not present in the response map
				}
				statuses[n] = &periscopepb.StreamStatusResponse{}
			}
			return &periscopepb.StreamsStatusResponse{Statuses: statuses}, nil
		},
	}
	l := NewStreamMetricsLoader(fake)

	res, err := l.LoadMany(context.Background(), "t1", []string{"a", "b", "a", "absent"})
	if err != nil {
		t.Fatal(err)
	}
	// Dedup is against the cache, not within the batch.
	if len(requested) != 4 {
		t.Fatalf("batch requested %v, want all 4 forwarded", requested)
	}
	// Only names present in the response map are returned; absent is not keyed
	// (this loader does not nil-prime misses, unlike Stream/Artifact loaders).
	if res["a"] == nil || res["b"] == nil {
		t.Fatalf("expected a,b present: %v", res)
	}
	if _, ok := res["absent"]; ok {
		t.Fatalf("absent should not be in results: %v", res)
	}

	// "a"/"b" are cached; "absent" was never cached, so it is re-fetched.
	fake.Calls = 0
	requested = nil
	if _, err := l.LoadMany(context.Background(), "t1", []string{"a", "b", "absent"}); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 1 || len(requested) != 1 || requested[0] != "absent" {
		t.Fatalf("second LoadMany should re-fetch only [absent], got calls=%d requested=%v", fake.Calls, requested)
	}
}

func TestStreamMetrics_LoadManyAllCachedSkipsBackend(t *testing.T) {
	fake := &clientstest.FakePeriscope{
		GetStreamStatusFn: func(context.Context, string, string) (*periscopepb.StreamStatusResponse, error) {
			return &periscopepb.StreamStatusResponse{}, nil
		},
	}
	l := NewStreamMetricsLoader(fake)
	if _, err := l.Load(context.Background(), "t1", "a"); err != nil {
		t.Fatal(err)
	}
	fake.Calls = 0
	if _, err := l.LoadMany(context.Background(), "t1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 0 {
		t.Fatalf("fully-cached LoadMany hit backend %d times, want 0", fake.Calls)
	}
}

func TestStreamMetrics_LoadPropagatesError(t *testing.T) {
	sentinel := errors.New("periscope down")
	fake := &clientstest.FakePeriscope{
		GetStreamStatusFn: func(context.Context, string, string) (*periscopepb.StreamStatusResponse, error) {
			return nil, sentinel
		},
	}
	l := NewStreamMetricsLoader(fake)
	if _, err := l.Load(context.Background(), "t1", "a"); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
}
