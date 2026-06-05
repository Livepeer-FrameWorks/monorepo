package graph

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/api_gateway/graph/model"
)

func strPtr(v string) *string { return &v }

func TestParseUnixNanoPart(t *testing.T) {
	const nanos int64 = 1_700_000_000_123_456_789
	got, err := parseUnixNanoPart("1700000000123456789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UnixNano() != nanos || got.Location() != time.UTC {
		t.Errorf("parseUnixNanoPart = %v (unixnano %d), want %d UTC", got, got.UnixNano(), nanos)
	}
	if _, err := parseUnixNanoPart("not-a-number"); err == nil {
		t.Error("expected error for non-numeric part")
	}
}

func TestTimeRangeAround(t *testing.T) {
	if timeRangeAround(time.Time{}, time.Hour) != nil {
		t.Error("zero timestamp must yield nil range")
	}
	ts := time.Unix(1_700_000_000, 0).UTC()
	tr := timeRangeAround(ts, time.Hour)
	if tr == nil {
		t.Fatal("expected a range")
	}
	if !tr.Start.Equal(ts.Add(-time.Hour)) || !tr.End.Equal(ts.Add(time.Hour)) {
		t.Errorf("range = [%v, %v], want ±1h around %v", tr.Start, tr.End, ts)
	}
}

func TestTimesClose(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{"both zero", time.Time{}, time.Time{}, true},
		{"one zero", base, time.Time{}, false},
		{"identical", base, base, true},
		{"within 1s", base, base.Add(500 * time.Millisecond), true},
		{"exactly 1s", base, base.Add(time.Second), true},
		{"over 1s", base, base.Add(2 * time.Second), false},
		{"negative within", base, base.Add(-900 * time.Millisecond), true},
	}
	for _, tc := range cases {
		if got := timesClose(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: timesClose = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEncodeTimeParts(t *testing.T) {
	if got := encodeTimePart(time.Time{}); got != "0" {
		t.Errorf("zero time → %q, want \"0\"", got)
	}
	tm := time.Unix(0, 12345).UTC()
	if got := encodeTimePart(tm); got != "12345" {
		t.Errorf("encodeTimePart = %q, want 12345", got)
	}
	if got := encodeProtoTimestampPart(nil); got != "0" {
		t.Errorf("nil proto ts → %q, want \"0\"", got)
	}
	if got := encodeProtoTimestampPart(timestamppb.New(tm)); got != "12345" {
		t.Errorf("encodeProtoTimestampPart = %q, want 12345", got)
	}
}

func TestMergeConnectionInput(t *testing.T) {
	// nil page leaves the explicit args untouched.
	f, a, l, b := mergeConnectionInput(nil, intPtr(5), strPtr("cursor"), nil, nil)
	if *f != 5 || *a != "cursor" || l != nil || b != nil {
		t.Errorf("nil page mutated args: %v %v %v %v", f, a, l, b)
	}
	// page fields override.
	page := &model.ConnectionInput{First: intPtr(20), After: strPtr("p-after"), Last: intPtr(3), Before: strPtr("p-before")}
	f, a, l, b = mergeConnectionInput(page, nil, nil, nil, nil)
	if *f != 20 || *a != "p-after" || *l != 3 || *b != "p-before" {
		t.Errorf("page values not applied: %v %v %v %v", *f, *a, *l, *b)
	}
}

func TestMergeForwardConnectionInput(t *testing.T) {
	f, a := mergeForwardConnectionInput(nil, intPtr(7), strPtr("c"))
	if *f != 7 || *a != "c" {
		t.Errorf("nil page mutated args: %v %v", *f, *a)
	}
	page := &model.ConnectionInput{First: intPtr(40), After: strPtr("aft")}
	f, a = mergeForwardConnectionInput(page, intPtr(7), nil)
	if *f != 40 || *a != "aft" {
		t.Errorf("forward page not applied: %v %v", *f, *a)
	}
}
