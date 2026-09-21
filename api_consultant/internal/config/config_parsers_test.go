package config

import (
	"reflect"
	"testing"
)

// ParseRateLimitOverrides parses a CSV of tenant:limit pairs, skipping any
// malformed or negative entry rather than failing the whole map.
func TestParseRateLimitOverrides(t *testing.T) {
	got := ParseRateLimitOverrides(" t1:10 , t2:20 , bad , t3:notnum , t4:-5 , :5 , t5:0 ")
	want := map[string]int{"t1": 10, "t2": 20, "t5": 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseRateLimitOverrides = %v, want %v", got, want)
	}
	if got := ParseRateLimitOverrides(""); len(got) != 0 {
		t.Errorf("empty input should yield empty map, got %v", got)
	}
}
