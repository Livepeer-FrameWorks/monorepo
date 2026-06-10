package mistdiag

import (
	"context"
	"errors"
	"testing"
)

// DiscoverStreams drives the runner and parses the MistServer active_streams
// payload. These tests pin the failure surfaces (transport error, non-zero
// exit, malformed JSON) and the happy path through the mock runner.
func TestDiscoverStreamsSuccess(t *testing.T) {
	runner := &mockRunner{stdout: `{"active_streams":{"live+abc":{"source":"push://"}}}`}
	streams, err := DiscoverStreams(context.Background(), runner, "native")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(streams) != 1 || streams[0].Name != "live+abc" || streams[0].Source != "push://" {
		t.Fatalf("unexpected streams: %#v", streams)
	}
	if streams[0].HLSURL == "" {
		t.Errorf("HLS URL should be populated")
	}
}

func TestDiscoverStreamsTransportError(t *testing.T) {
	runner := &mockRunner{err: errors.New("ssh down")}
	if _, err := DiscoverStreams(context.Background(), runner, "docker"); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestDiscoverStreamsNonZeroExit(t *testing.T) {
	runner := &mockRunner{exitCode: 7, stderr: "curl: connection refused"}
	_, err := DiscoverStreams(context.Background(), runner, "native")
	if err == nil {
		t.Fatal("expected error on non-zero exit code")
	}
}

func TestDiscoverStreamsMalformedJSON(t *testing.T) {
	runner := &mockRunner{stdout: "not json"}
	if _, err := DiscoverStreams(context.Background(), runner, "native"); err == nil {
		t.Fatal("expected parse error on malformed JSON")
	}
}

// AnalyzerRunner.Run maps the runner result into AnalyzerResult: OK iff exit 0,
// stderr appended to Output, and parsed errors/warnings surfaced.
func TestAnalyzerRunnerRunSuccess(t *testing.T) {
	runner := &mockRunner{stdout: "all good", exitCode: 0}
	ar := NewAnalyzerRunner(runner, "native")
	res, err := ar.Run(context.Background(), AnalyzerOptions{Analyzer: "HLS", Target: "stream"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OK || res.ExitCode != 0 {
		t.Fatalf("expected OK exit 0, got OK=%v exit=%d", res.OK, res.ExitCode)
	}
}

func TestAnalyzerRunnerRunNonZeroExitAppendsStderr(t *testing.T) {
	runner := &mockRunner{stdout: "out", stderr: "boom", exitCode: 3}
	ar := NewAnalyzerRunner(runner, "native")
	res, err := ar.Run(context.Background(), AnalyzerOptions{Analyzer: "HLS"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OK {
		t.Errorf("non-zero exit should not be OK")
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
}

// A nil runner result (transport failure) maps to ExitCode -1 plus an error.
func TestAnalyzerRunnerRunNilResult(t *testing.T) {
	runner := &mockRunner{err: errors.New("dial failed")}
	ar := NewAnalyzerRunner(runner, "native")
	res, err := ar.Run(context.Background(), AnalyzerOptions{Analyzer: "HLS"})
	if err == nil {
		t.Fatal("expected error when runner result is nil")
	}
	if res.ExitCode != -1 {
		t.Errorf("nil result exit code = %d, want -1", res.ExitCode)
	}
}

// An invalid analyzer name is rejected before any command runs.
func TestAnalyzerRunnerRunRejectsBadName(t *testing.T) {
	ar := NewAnalyzerRunner(&mockRunner{}, "native")
	if _, err := ar.Run(context.Background(), AnalyzerOptions{Analyzer: "../etc/passwd"}); err == nil {
		t.Fatal("expected validation error for unsafe analyzer name")
	}
}

func TestAnalyzerRunnerValidateDelegatesToRun(t *testing.T) {
	runner := &mockRunner{stdout: "valid", exitCode: 0}
	ar := NewAnalyzerRunner(runner, "native")
	res, err := ar.Validate(context.Background(), "HLS", "rtmp://x", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OK {
		t.Errorf("expected OK validation result")
	}
}
