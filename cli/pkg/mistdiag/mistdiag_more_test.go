package mistdiag

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBuildCommand_DetailBoundary(t *testing.T) {
	t.Parallel()
	ar := &AnalyzerRunner{mode: "native"}
	cases := []struct {
		detail int
		want   string
	}{
		{-1, "--detail 0"},  // < 0 clamps up to 0
		{0, "--detail 0"},   // exactly 0 unchanged
		{1, "--detail 1"},   // just above floor
		{9, "--detail 9"},   // just below ceiling
		{10, "--detail 10"}, // exactly maxDetail unchanged
		{11, "--detail 10"}, // > maxDetail clamps down
	}
	for _, tc := range cases {
		cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Detail: tc.detail})
		if !strings.Contains(cmd, tc.want) {
			t.Errorf("detail=%d: want %q in %q", tc.detail, tc.want, cmd)
		}
	}
}

func TestBuildCommand_TimeoutBoundary(t *testing.T) {
	t.Parallel()
	ar := &AnalyzerRunner{mode: "native"}
	// timeout==0 → no --timeout flag at all.
	if cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Timeout: 0}); strings.Contains(cmd, "--timeout") {
		t.Errorf("timeout=0 must omit --timeout; got %q", cmd)
	}
	// negative → clamped to 0 → omitted.
	if cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Timeout: -5}); strings.Contains(cmd, "--timeout") {
		t.Errorf("negative timeout must omit --timeout; got %q", cmd)
	}
	// 1 → present.
	if cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Timeout: 1}); !strings.Contains(cmd, "--timeout 1") {
		t.Errorf("timeout=1 must emit --timeout 1; got %q", cmd)
	}
	// exactly maxTimeout unchanged.
	if cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Timeout: 300}); !strings.Contains(cmd, "--timeout 300") {
		t.Errorf("timeout=300 must stay 300; got %q", cmd)
	}
	// 299 just below ceiling unchanged.
	if cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Timeout: 299}); !strings.Contains(cmd, "--timeout 299") {
		t.Errorf("timeout=299 must stay 299; got %q", cmd)
	}
	// 301 clamps to 300.
	if cmd := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "x", Timeout: 301}); !strings.Contains(cmd, "--timeout 300") {
		t.Errorf("timeout=301 must clamp to 300; got %q", cmd)
	}
}

func TestBuildCommand_TargetGate(t *testing.T) {
	t.Parallel()
	ar := &AnalyzerRunner{mode: "native"}
	// Empty target → no quoted target appended.
	empty := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: ""})
	if strings.Contains(empty, "''") || strings.HasSuffix(strings.TrimSpace(empty), "'") {
		t.Errorf("empty target must not append a quoted arg; got %q", empty)
	}
	// "-" (stdin) → not appended as a quoted path.
	dash := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "-"})
	if strings.Contains(dash, "'-'") {
		t.Errorf("dash target must not be appended as quoted; got %q", dash)
	}
	// Real target → appended single-quoted.
	real := ar.buildCommand(AnalyzerOptions{Analyzer: "HLS", Target: "http://x/y"})
	if !strings.Contains(real, "'http://x/y'") {
		t.Errorf("real target must be quoted+appended; got %q", real)
	}
}

func TestNewAnalyzerRunner_ModeNormalization(t *testing.T) {
	t.Parallel()
	// Only "docker" stays docker; everything else normalizes to native.
	if ar := NewAnalyzerRunner(&mockRunner{}, "docker"); ar.mode != "docker" {
		t.Errorf("docker must stay docker; got %q", ar.mode)
	}
	for _, m := range []string{"native", "", "DOCKER", "podman", "k8s"} {
		if ar := NewAnalyzerRunner(&mockRunner{}, m); ar.mode != "native" {
			t.Errorf("mode %q must normalize to native; got %q", m, ar.mode)
		}
	}
}

func TestAvailable_ErrorWithNilResult(t *testing.T) {
	t.Parallel()
	// runner returns (nil, err) → Available must surface the error.
	ar := NewAnalyzerRunner(&mockRunner{err: errors.New("ssh down")}, "native")
	_, err := ar.Available(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed to list analyzers") {
		t.Fatalf("expected wrapped list error; got %v", err)
	}
}

func TestSummary_WarningsBoundary(t *testing.T) {
	t.Parallel()
	// OK + zero warnings → "OK"
	if got := (&AnalyzerResult{OK: true}).Summary(); got != "OK" {
		t.Errorf("OK no warnings: got %q want OK", got)
	}
	// OK + one warning → "OK (with warnings)"
	withWarn := &AnalyzerResult{OK: true, Warnings: []string{"w"}}
	if got := withWarn.Summary(); got != "OK (with warnings)" {
		t.Errorf("OK one warning: got %q", got)
	}
	// not OK + errors → first error
	failErr := &AnalyzerResult{OK: false, Errors: []string{"boom"}}
	if got := failErr.Summary(); got != "boom" {
		t.Errorf("fail with error: got %q want boom", got)
	}
	// not OK + no errors → FAIL
	if got := (&AnalyzerResult{OK: false}).Summary(); got != "FAIL" {
		t.Errorf("fail no error: got %q want FAIL", got)
	}
}
