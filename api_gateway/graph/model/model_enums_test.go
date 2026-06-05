package model

import (
	"strings"
	"testing"
)

func TestNodeOperationalMode_IsValidAndMarshal(t *testing.T) {
	for _, m := range AllNodeOperationalMode {
		if !m.IsValid() {
			t.Errorf("%s should be valid", m)
		}
		var b strings.Builder
		m.MarshalGQL(&b)
		if got := b.String(); got != `"`+string(m)+`"` {
			t.Errorf("MarshalGQL(%s) = %s, want quoted", m, got)
		}
		if m.String() != string(m) {
			t.Errorf("String(%s) mismatch", m)
		}
	}
	if NodeOperationalMode("BOGUS").IsValid() || NodeOperationalMode("").IsValid() {
		t.Error("unknown/empty mode must be invalid")
	}
}

func TestNodeOperationalMode_UnmarshalGQL(t *testing.T) {
	var e NodeOperationalMode
	if err := e.UnmarshalGQL("DRAINING"); err != nil || e != NodeOperationalModeDraining {
		t.Fatalf("UnmarshalGQL(DRAINING) = (%v, %v), want (DRAINING, nil)", e, err)
	}
	if err := e.UnmarshalGQL("bogus"); err == nil {
		t.Error("invalid enum string must error")
	}
	if err := e.UnmarshalGQL(123); err == nil {
		t.Error("non-string must error")
	}
}

// TestNodeOperationalMode_WireRoundTrip pins the GraphQL<->Foghorn wire mapping,
// including the contract that an empty wire string defaults to NORMAL and an
// unknown wire string is rejected.
func TestNodeOperationalMode_WireRoundTrip(t *testing.T) {
	wire := map[NodeOperationalMode]string{
		NodeOperationalModeNormal:      "normal",
		NodeOperationalModeDraining:    "draining",
		NodeOperationalModeMaintenance: "maintenance",
	}
	for mode, want := range wire {
		if got := mode.WireValue(); got != want {
			t.Errorf("WireValue(%s) = %q, want %q", mode, got, want)
		}
		// Round-trip: wire string parses back to the same enum.
		if back, ok := NodeOperationalModeFromWire(want); !ok || back != mode {
			t.Errorf("FromWire(%q) = (%s, %v), want (%s, true)", want, back, ok, mode)
		}
	}
	if NodeOperationalMode("BOGUS").WireValue() != "" {
		t.Error("invalid mode WireValue must be empty")
	}
	if m, ok := NodeOperationalModeFromWire(""); !ok || m != NodeOperationalModeNormal {
		t.Errorf("FromWire(\"\") = (%s, %v), want (NORMAL, true)", m, ok)
	}
	if _, ok := NodeOperationalModeFromWire("offline"); ok {
		t.Error("unknown wire string must return ok=false")
	}
}

func TestMediaRetentionTarget_Enum(t *testing.T) {
	for _, target := range AllMediaRetentionTarget {
		if !target.IsValid() {
			t.Errorf("%s should be valid", target)
		}
		var b strings.Builder
		target.MarshalGQL(&b)
		if got := b.String(); got != `"`+string(target)+`"` {
			t.Errorf("MarshalGQL(%s) = %s, want quoted", target, got)
		}
		if target.String() != string(target) {
			t.Errorf("String(%s) mismatch", target)
		}
	}
	if MediaRetentionTarget("HLS").IsValid() {
		t.Error("unknown target must be invalid")
	}

	var e MediaRetentionTarget
	if err := e.UnmarshalGQL("VOD"); err != nil || e != MediaRetentionTargetVod {
		t.Fatalf("UnmarshalGQL(VOD) = (%v, %v), want (VOD, nil)", e, err)
	}
	if err := e.UnmarshalGQL("nope"); err == nil {
		t.Error("invalid target string must error")
	}
	if err := e.UnmarshalGQL(42); err == nil {
		t.Error("non-string must error")
	}
}

// TestSkipperChatEvent_UnionMembership pins that each concrete type is a member
// of the SkipperChatEvent GraphQL union (the resolver relies on this to route
// union values).
func TestSkipperChatEvent_UnionMembership(t *testing.T) {
	members := []SkipperChatEvent{
		SkipperToken{},
		SkipperToolStartEvent{},
		SkipperToolEndEvent{},
		SkipperMeta{},
		SkipperDone{},
	}
	for _, m := range members {
		m.IsSkipperChatEvent() // marker; exercises the method body
	}
	if len(members) != 5 {
		t.Fatalf("expected 5 union members, got %d", len(members))
	}
}
