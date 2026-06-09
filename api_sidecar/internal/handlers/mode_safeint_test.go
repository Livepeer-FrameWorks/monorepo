package handlers

import (
	"math"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// parseMode maps the node-mode admin endpoint's string input to the proto enum,
// case-insensitively, and rejects anything it doesn't recognise. It must
// round-trip with modeToString for every valid mode.
func TestParseMode(t *testing.T) {
	valid := map[string]ipcpb.NodeOperationalMode{
		"normal":      ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL,
		"draining":    ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING,
		"maintenance": ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE,
	}
	for s, want := range valid {
		got, ok := parseMode(s)
		if !ok || got != want {
			t.Fatalf("parseMode(%q) = %v,%v want %v,true", s, got, ok, want)
		}
		// Case-insensitive.
		if up, ok := parseMode(upper(s)); !ok || up != want {
			t.Fatalf("parseMode(%q) = %v,%v want %v,true", upper(s), up, ok, want)
		}
		// Round-trips back to the canonical string.
		if rt := modeToString(want); rt != s {
			t.Fatalf("modeToString(%v) = %q, want %q", want, rt, s)
		}
	}

	if got, ok := parseMode("bogus"); ok || got != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		t.Fatalf("parseMode(bogus) = %v,%v want UNSPECIFIED,false", got, ok)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// safeInt32 clamps an int into the int32 range. The invariant matters because
// the result feeds protobuf int32 fields where a silent wrap would corrupt a
// metric or count.
func TestSafeInt32(t *testing.T) {
	cases := []struct {
		in   int
		want int32
	}{
		{0, 0},
		{42, 42},
		{-42, -42},
		{math.MaxInt32, math.MaxInt32},
		{math.MinInt32, math.MinInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{math.MinInt32 - 1, math.MinInt32},
	}
	for _, c := range cases {
		if got := safeInt32(c.in); got != c.want {
			t.Errorf("safeInt32(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
