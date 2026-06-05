package knowledge

import "testing"

// DeduplicateBySource caps a result set to `limit` while enforcing a
// per-source ceiling (`maxPerSource`), preserving the input order. This keeps a
// single noisy source from crowding out diverse results in the reranked set.
func TestDeduplicateBySource(t *testing.T) {
	mk := func(src string) Chunk { return Chunk{SourceURL: src} }

	t.Run("under limit returns input unchanged", func(t *testing.T) {
		in := []Chunk{mk("a"), mk("a"), mk("a")}
		got := DeduplicateBySource(in, 5, 1)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3 (fast path keeps all when <= limit)", len(got))
		}
	})

	t.Run("enforces per-source cap and order", func(t *testing.T) {
		in := []Chunk{mk("a"), mk("b"), mk("a"), mk("a"), mk("b"), mk("c")}
		got := DeduplicateBySource(in, 4, 1) // one per source
		want := []string{"a", "b", "c"}      // a,b admitted; later a,b skipped; c admitted
		if len(got) != len(want) {
			t.Fatalf("len = %d (%v), want %d (%v)", len(got), urls(got), len(want), want)
		}
		for i, w := range want {
			if got[i].SourceURL != w {
				t.Errorf("position %d = %q, want %q (order must be preserved)", i, got[i].SourceURL, w)
			}
		}
	})

	t.Run("stops at limit", func(t *testing.T) {
		in := []Chunk{mk("a"), mk("b"), mk("c"), mk("d")}
		got := DeduplicateBySource(in, 2, 5)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (hard limit)", len(got))
		}
	})
}

func urls(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.SourceURL
	}
	return out
}
