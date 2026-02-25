package mistdiag

import (
	"testing"
)

func TestParseOutput_Pass(t *testing.T) {
	result := ParseOutput("Segment 1 OK\nSegment 2 OK\nDone.", "", 0)
	if !result.OK {
		t.Error("expected OK=true for exit code 0")
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

func TestParseOutput_Fail(t *testing.T) {
	result := ParseOutput("Segment 1 OK\nERROR: segment timing gap\nDone.", "", 1)
	if result.OK {
		t.Error("expected OK=false for exit code 1")
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if result.Errors[0] != "ERROR: segment timing gap" {
		t.Errorf("unexpected error: %q", result.Errors[0])
	}
}

func TestParseOutput_Warnings(t *testing.T) {
	result := ParseOutput("WARNING: audio only stream\nDone.", "", 0)
	if !result.OK {
		t.Error("expected OK=true for exit code 0")
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(result.Warnings))
	}
}

func TestParseOutput_StderrErrors(t *testing.T) {
	result := ParseOutput("some output", "FATAL: cannot connect", 1)
	if result.OK {
		t.Error("expected OK=false")
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error from stderr, got %d", len(result.Errors))
	}
}

func TestSummary(t *testing.T) {
	tests := []struct {
		name   string
		result AnalyzerResult
		want   string
	}{
		{"pass", AnalyzerResult{OK: true}, "OK"},
		{"pass_with_warn", AnalyzerResult{OK: true, Warnings: []string{"w"}}, "OK (with warnings)"},
		{"fail_with_err", AnalyzerResult{OK: false, Errors: []string{"bad segment"}}, "bad segment"},
		{"fail_no_err", AnalyzerResult{OK: false}, "FAIL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.Summary()
			if got != tt.want {
				t.Errorf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFirstError(t *testing.T) {
	r := &AnalyzerResult{Errors: []string{"first", "second"}}
	if r.FirstError() != "first" {
		t.Errorf("FirstError() = %q, want %q", r.FirstError(), "first")
	}

	r2 := &AnalyzerResult{}
	if r2.FirstError() != "" {
		t.Errorf("FirstError() should be empty for no errors, got %q", r2.FirstError())
	}
}
