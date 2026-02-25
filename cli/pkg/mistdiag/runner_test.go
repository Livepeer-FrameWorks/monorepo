package mistdiag

import (
	"context"
	"testing"
)

func TestValidateAnalyzerName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"HLS", false},
		{"hls", false},
		{"TS", false},
		{"RTMP", false},
		{"H264", false},
		{"unknown", true},
		{"", true},
		{"../evil", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAnalyzerName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAnalyzerName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeAnalyzerName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hls", "HLS"},
		{"HLS", "HLS"},
		{"ts", "TS"},
		{"h264", "H264"},
		{"rtmp", "RTMP"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeAnalyzerName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeAnalyzerName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildCommand_Docker(t *testing.T) {
	ar := &AnalyzerRunner{mode: "docker", container: "mistserver"}
	cmd := ar.buildCommand(AnalyzerOptions{
		Analyzer: "HLS",
		Target:   "http://localhost:8080/hls/live/index.m3u8",
		Detail:   2,
		Validate: true,
		Timeout:  10,
	})

	// Should wrap in docker exec
	if !contains(cmd, "docker exec") {
		t.Errorf("docker mode should wrap command with docker exec, got: %s", cmd)
	}
	if !contains(cmd, "MistAnalyserHLS") {
		t.Errorf("command should contain MistAnalyserHLS, got: %s", cmd)
	}
	if !contains(cmd, "-V") {
		t.Errorf("command should contain -V for validate, got: %s", cmd)
	}
	if !contains(cmd, "--timeout 10") {
		t.Errorf("command should contain --timeout 10, got: %s", cmd)
	}
}

func TestBuildCommand_Native(t *testing.T) {
	ar := &AnalyzerRunner{mode: "native", container: "mistserver"}
	cmd := ar.buildCommand(AnalyzerOptions{
		Analyzer: "TS",
		Target:   "/tmp/recording.ts",
		Detail:   5,
		Validate: false,
		Timeout:  0,
	})

	if contains(cmd, "docker exec") {
		t.Errorf("native mode should not use docker exec, got: %s", cmd)
	}
	if !contains(cmd, "MistAnalyserTS") {
		t.Errorf("command should contain MistAnalyserTS, got: %s", cmd)
	}
	if !contains(cmd, "--detail 5") {
		t.Errorf("command should contain --detail 5, got: %s", cmd)
	}
	if contains(cmd, "-V") {
		t.Errorf("command should not contain -V when validate=false, got: %s", cmd)
	}
	if contains(cmd, "--timeout") {
		t.Errorf("command should not contain --timeout when timeout=0, got: %s", cmd)
	}
}

func TestBuildCommand_ClampDetail(t *testing.T) {
	ar := &AnalyzerRunner{mode: "native"}
	cmd := ar.buildCommand(AnalyzerOptions{
		Analyzer: "HLS",
		Target:   "http://example.com",
		Detail:   99,
	})

	if !contains(cmd, "--detail 10") {
		t.Errorf("detail should be clamped to 10, got: %s", cmd)
	}
}

func TestBuildCommand_ClampTimeout(t *testing.T) {
	ar := &AnalyzerRunner{mode: "native"}
	cmd := ar.buildCommand(AnalyzerOptions{
		Analyzer: "HLS",
		Target:   "http://example.com",
		Timeout:  999,
	})

	if !contains(cmd, "--timeout 300") {
		t.Errorf("timeout should be clamped to 300, got: %s", cmd)
	}
}

func TestAvailable_ParsesOutput(t *testing.T) {
	runner := &mockRunner{
		stdout: "/usr/local/bin/MistAnalyserHLS\n/usr/local/bin/MistAnalyserTS\n/usr/local/bin/MistAnalyserRTMP\n",
	}
	ar := NewAnalyzerRunner(runner, "native")

	names, err := ar.Available(context.Background())
	if err != nil {
		t.Fatalf("Available() error = %v", err)
	}

	if len(names) != 3 {
		t.Fatalf("Expected 3 analyzers, got %d: %v", len(names), names)
	}

	want := map[string]bool{"HLS": true, "TS": true, "RTMP": true}
	for _, name := range names {
		if !want[name] {
			t.Errorf("unexpected analyzer: %s", name)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
