package mistdiag

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	fwssh "frameworks/cli/pkg/ssh"
)

// Known analyzer names that ship with MistServer.
var KnownAnalyzers = []string{
	"AV1", "DTSC", "EBML", "FLAC", "FLV", "H264",
	"HLS", "MP4", "OGG", "RIFF", "RTMP", "RTSP", "TS",
}

const (
	analyzerPrefix = "MistAnalyser"
	// Native installs put analyzers on PATH under /usr/local/bin; the edge
	// image seeds the Mist tree under /opt/frameworks/mistserver (bin/ with
	// bundled lib/, so container invocations need LD_LIBRARY_PATH).
	nativeBinDir    = "/usr/local/bin"
	containerBinDir = "/opt/frameworks/mistserver/bin"
	containerLibDir = "/opt/frameworks/mistserver/lib"
	maxDetail       = 10
	maxTimeout      = 300
)

// AnalyzerRunner executes MistServer analyzer binaries on an edge node.
type AnalyzerRunner struct {
	runner    fwssh.Runner
	mode      string // "container" or "native"
	container string
	binDir    string
}

// AnalyzerOptions configures an analyzer invocation.
type AnalyzerOptions struct {
	Analyzer string // e.g. "HLS", "TS", "RTMP"
	Target   string // URL, file path, or "-" for stdin
	Detail   int    // 0-10
	Validate bool
	Timeout  int // seconds (0 = no timeout)
}

// AnalyzerResult holds parsed analyzer output.
type AnalyzerResult struct {
	OK       bool
	Output   string
	Errors   []string
	Warnings []string
	ExitCode int
	Duration time.Duration
}

// NewAnalyzerRunner creates a runner for the given deploy mode: "container"
// (single edge image; "docker" is a deprecated alias) execs into the
// frameworks-edge container, anything else runs natively on the host.
func NewAnalyzerRunner(runner fwssh.Runner, mode string) *AnalyzerRunner {
	if mode == "container" || mode == "docker" {
		return &AnalyzerRunner{runner: runner, mode: "container", container: "frameworks-edge", binDir: containerBinDir}
	}
	return &AnalyzerRunner{runner: runner, mode: "native", binDir: nativeBinDir}
}

// Available returns the analyzer names present on the node.
func (ar *AnalyzerRunner) Available(ctx context.Context) ([]string, error) {
	cmd := fmt.Sprintf("ls %s/%s* 2>/dev/null", ar.binDir, analyzerPrefix)
	cmd = ar.wrapCommand(cmd)

	result, err := ar.runner.Run(ctx, cmd)
	if err != nil && result == nil {
		return nil, fmt.Errorf("failed to list analyzers: %w", err)
	}

	var names []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		base := filepath.Base(line)
		if strings.HasPrefix(base, analyzerPrefix) {
			name := strings.TrimPrefix(base, analyzerPrefix)
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// Run executes an analyzer with the given options and returns parsed results.
func (ar *AnalyzerRunner) Run(ctx context.Context, opts AnalyzerOptions) (*AnalyzerResult, error) {
	if err := validateAnalyzerName(opts.Analyzer); err != nil {
		return nil, err
	}

	cmd := ar.buildCommand(opts)
	start := time.Now()
	result, runErr := ar.runner.Run(ctx, cmd)

	ar_result := &AnalyzerResult{
		Duration: time.Since(start),
	}

	if result == nil {
		ar_result.ExitCode = -1
		return ar_result, fmt.Errorf("analyzer execution failed: %w", runErr)
	}

	ar_result.ExitCode = result.ExitCode
	ar_result.Output = result.Stdout
	if result.Stderr != "" {
		ar_result.Output += "\n" + result.Stderr
	}
	ar_result.OK = result.ExitCode == 0

	parsed := ParseOutput(result.Stdout, result.Stderr, result.ExitCode)
	ar_result.Errors = parsed.Errors
	ar_result.Warnings = parsed.Warnings

	return ar_result, nil
}

// Validate runs an analyzer in --validate mode against a target.
func (ar *AnalyzerRunner) Validate(ctx context.Context, analyzer, target string, timeout int) (*AnalyzerResult, error) {
	return ar.Run(ctx, AnalyzerOptions{
		Analyzer: analyzer,
		Target:   target,
		Detail:   2,
		Validate: true,
		Timeout:  timeout,
	})
}

func (ar *AnalyzerRunner) buildCommand(opts AnalyzerOptions) string {
	binary := fmt.Sprintf("%s/%s%s", ar.binDir, analyzerPrefix, opts.Analyzer)

	var args []string

	detail := opts.Detail
	if detail < 0 {
		detail = 0
	}
	if detail > maxDetail {
		detail = maxDetail
	}
	args = append(args, fmt.Sprintf("--detail %d", detail))

	if opts.Validate {
		args = append(args, "-V")
	}

	timeout := opts.Timeout
	if timeout < 0 {
		timeout = 0
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	if timeout > 0 {
		args = append(args, fmt.Sprintf("--timeout %d", timeout))
	}

	if opts.Target != "" && opts.Target != "-" {
		args = append(args, fwssh.ShellQuote(opts.Target))
	}

	cmd := fmt.Sprintf("%s %s", binary, strings.Join(args, " "))
	return ar.wrapCommand(cmd)
}

// wrapCommand prefixes a command with docker exec when in container mode.
// The bundled Mist lib dir rides along because the seeded analyzers link
// against it (same contract as the mistserver run script).
func (ar *AnalyzerRunner) wrapCommand(cmd string) string {
	if ar.mode == "container" {
		cmd = fmt.Sprintf("LD_LIBRARY_PATH=%s %s", containerLibDir, cmd)
		return fmt.Sprintf("docker exec %s sh -c %s", fwssh.ShellQuote(ar.container), fwssh.ShellQuote(cmd))
	}
	return cmd
}

func validateAnalyzerName(name string) error {
	for _, known := range KnownAnalyzers {
		if strings.EqualFold(name, known) {
			return nil
		}
	}
	return fmt.Errorf("unknown analyzer %q (known: %s)", name, strings.Join(KnownAnalyzers, ", "))
}

// NormalizeAnalyzerName returns the canonical casing for an analyzer name.
func NormalizeAnalyzerName(name string) string {
	for _, known := range KnownAnalyzers {
		if strings.EqualFold(name, known) {
			return known
		}
	}
	return name
}
