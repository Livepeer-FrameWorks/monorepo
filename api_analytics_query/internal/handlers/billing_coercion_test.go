package handlers

import (
	"encoding/json"
	"math"
	"testing"
)

// floatFromJSONValue is the single numeric-coercion gate every projection
// divergence delta flows through. A wrong branch here silently mis-bills:
// a dropped uint64 byte count under-bills egress, a precision slip on a
// large media-seconds value mis-credits an operator. These cases pin the
// exact contract each input type honours.
func TestFloatFromJSONValue(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    float64
		wantErr bool
	}{
		{"float64", float64(12.5), 12.5, false},
		{"int", int(7), 7, false},
		{"int64", int64(-9), -9, false},
		{"uint32", uint32(4000000000), 4000000000, false},
		{"uint64", uint64(18000000000), 18000000000, false},
		{"json.Number integer", json.Number("42"), 42, false},
		{"json.Number decimal", json.Number("0.0000001"), 0.0000001, false},
		{"string decimal", "123.45", 123.45, false},
		{"string negative", "-0.5", -0.5, false},
		{"unparseable string", "not-a-number", 0, true},
		{"bool unsupported", true, 0, true},
		{"nil unsupported", nil, 0, true},
		{"map unsupported", map[string]any{}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := floatFromJSONValue(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// json.Number and string paths both delegate to strconv.ParseFloat, which
// ACCEPTS "NaN"/"Inf" without error. floatFromJSONValue therefore does NOT
// reject non-finite values — they pass through and are scrubbed later by
// sanitizeFloat at the adjustment-emit site (billing.go ~1102). This pins
// that division of responsibility so a future "reject here" change is a
// conscious decision, not an accident.
func TestFloatFromJSONValue_NonFiniteNotRejectedHere(t *testing.T) {
	got, err := floatFromJSONValue(json.Number("NaN"))
	if err != nil {
		t.Fatalf("json.Number NaN unexpectedly errored: %v", err)
	}
	if !math.IsNaN(got) {
		t.Errorf("got %v, want NaN passed through", got)
	}

	gotStr, err := floatFromJSONValue("Inf")
	if err != nil {
		t.Fatalf("string Inf unexpectedly errored: %v", err)
	}
	if !math.IsInf(gotStr, 1) {
		t.Errorf("got %v, want +Inf passed through", gotStr)
	}
}

// A float64 cannot represent every int64. The billing path stores byte
// counts and media-seconds that can exceed 2^53; coercion is lossy past
// that point. This documents the boundary so nobody assumes exact int64
// fidelity through the divergence math.
func TestFloatFromJSONValue_Int64PrecisionBoundary(t *testing.T) {
	const exactLimit = int64(1) << 53 // 9007199254740992, representable
	got, err := floatFromJSONValue(exactLimit)
	if err != nil || got != float64(exactLimit) {
		t.Fatalf("at 2^53 want exact, got %v err %v", got, err)
	}
	// 2^53 + 1 is NOT representable; it rounds down to 2^53.
	got, err = floatFromJSONValue(exactLimit + 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != float64(exactLimit) {
		t.Errorf("2^53+1 coerced to %v, expected rounding to %v (documented precision loss)", got, float64(exactLimit))
	}
}

// floatDeltaValues feeds every byte/time divergence delta. Its contract is
// narrow but load-bearing: return BOTH floats only when BOTH parse, and
// surface the first parse error without inventing a zero. A silent zero on
// a bad prior value would compute a full-magnitude (mis-signed) correction.
func TestFloatDeltaValues(t *testing.T) {
	prior, next, err := floatDeltaValues(json.Number("100"), json.Number("160"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prior != 100 || next != 160 {
		t.Errorf("got (%v,%v), want (100,160)", prior, next)
	}

	if _, _, err := floatDeltaValues("bad", json.Number("5")); err == nil {
		t.Error("bad prior value: want error, got nil")
	}
	if _, _, err := floatDeltaValues(json.Number("5"), "bad"); err == nil {
		t.Error("bad new value: want error, got nil")
	}
}

// BillableClusterID decides which cluster is credited for a viewer-metric row.
// The origin cluster (where the stream is produced) takes precedence over the
// serving cluster, so cross-cluster delivery bills the origin, not the edge.
// Flipping this precedence would mis-attribute egress between operators.
func TestBillableClusterID(t *testing.T) {
	// Origin present -> origin wins, even when a serving cluster is set.
	if got := (tenantViewerMetricRow{OriginClusterID: " origin ", ClusterID: "edge"}).BillableClusterID(); got != "origin" {
		t.Errorf("got %q, want trimmed origin (origin takes precedence)", got)
	}
	// No origin -> fall back to the serving cluster.
	if got := (tenantViewerMetricRow{OriginClusterID: "  ", ClusterID: " edge "}).BillableClusterID(); got != "edge" {
		t.Errorf("got %q, want trimmed edge fallback", got)
	}
	// Neither set -> empty (caller treats as unattributed).
	if got := (tenantViewerMetricRow{}).BillableClusterID(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
