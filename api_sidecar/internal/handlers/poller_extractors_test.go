package handlers

import (
	"sort"
	"testing"
)

// The poller decodes MistServer's untyped JSON (map[string]any), where every
// number arrives as float64. These extractors must coerce safely and fall back
// to the zero value on a nil or wrong-typed field rather than panicking.
func TestPollerScalarExtractors(t *testing.T) {
	t.Run("getFloat64", func(t *testing.T) {
		if got := getFloat64(float64(3.5)); got != 3.5 {
			t.Fatalf("getFloat64(3.5) = %v", got)
		}
		if got := getFloat64("nope"); got != 0 {
			t.Fatalf("getFloat64(string) = %v, want 0", got)
		}
		if got := getFloat64(nil); got != 0 {
			t.Fatalf("getFloat64(nil) = %v, want 0", got)
		}
	})

	t.Run("getInt64 accepts both float64 and int64", func(t *testing.T) {
		if got := getInt64(float64(42)); got != 42 {
			t.Fatalf("getInt64(float64 42) = %d, want 42", got)
		}
		if got := getInt64(int64(7)); got != 7 {
			t.Fatalf("getInt64(int64 7) = %d, want 7", got)
		}
		if got := getInt64("x"); got != 0 {
			t.Fatalf("getInt64(string) = %d, want 0", got)
		}
	})

	t.Run("getString", func(t *testing.T) {
		if got := getString("hi"); got != "hi" {
			t.Fatalf("getString = %q", got)
		}
		if got := getString(123); got != "" {
			t.Fatalf("getString(int) = %q, want empty", got)
		}
	})

	t.Run("getFloat64PointerValue", func(t *testing.T) {
		if got := getFloat64PointerValue(nil); got != 0 {
			t.Fatalf("nil pointer = %v, want 0", got)
		}
		v := 9.0
		if got := getFloat64PointerValue(&v); got != 9.0 {
			t.Fatalf("&9.0 = %v, want 9", got)
		}
	})
}

func TestGetMapKeys(t *testing.T) {
	if got := getMapKeys(nil); len(got) != 0 {
		t.Fatalf("getMapKeys(nil) = %#v, want empty", got)
	}
	got := getMapKeys(map[string]any{"b": 1, "a": 2})
	sort.Strings(got)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("getMapKeys = %#v, want keys a,b", got)
	}
}
