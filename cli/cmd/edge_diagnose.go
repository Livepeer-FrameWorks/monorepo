package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/mistdiag"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newEdgeDiagnoseCmd() *cobra.Command {
	var (
		sshTarget string
		sshKey    string
		dir       string
	)

	cmd := &cobra.Command{
		Use:   "diagnose <component>",
		Short: "Deep diagnostics for edge node components",
		Long: `Run diagnostic checks on edge node components.

Supported components:
  media    - MistServer stream analysis (HLS validation, codec checks, timing)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "media":
				return runDiagnoseMediaWrapper(cmd, sshTarget, sshKey, dir)
			default:
				return fmt.Errorf("unknown component: %s (supported: media)", args[0])
			}
		},
	}

	cmd.Flags().StringVar(&sshTarget, "ssh", "", "SSH target (user@host)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with edge templates")

	// Add media subcommand for flag-rich direct invocation
	cmd.AddCommand(newEdgeDiagnoseMediaCmd())

	return cmd
}

func newEdgeDiagnoseMediaCmd() *cobra.Command {
	var (
		sshTarget    string
		sshKey       string
		dir          string
		stream       string
		analyzer     string
		target       string
		detail       int
		timeout      int
		validateOnly bool
	)

	cmd := &cobra.Command{
		Use:   "media",
		Short: "MistServer stream analysis",
		Long: `Analyze media streams using MistServer's built-in analyzer tools.

Without --analyzer/--target, auto-discovers active streams and validates HLS output.
With --analyzer and --target, runs a specific analyzer against the given URL or file.

Available analyzers: HLS, RTMP, TS, H264, MP4, DTSC, EBML, FLV, FLAC, OGG, AV1, RIFF, RTSP`,
		Example: `  # Auto-discover and validate all active streams
  frameworks edge diagnose media

  # Validate streams on a remote edge node
  frameworks edge diagnose media --ssh root@edge-1

  # Run HLS analyzer on a specific URL
  frameworks edge diagnose media --analyzer HLS --target http://localhost:8080/hls/live/index.m3u8

  # Deep TS analysis of a recording
  frameworks edge diagnose media --analyzer TS --target /tmp/recording.ts --detail 5

  # Validate a single stream
  frameworks edge diagnose media --stream live+abc123`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if analyzer != "" && target != "" {
				return runDiagnoseMediaManual(cmd, sshTarget, sshKey, dir, analyzer, target, detail, timeout, validateOnly)
			}
			return runDiagnoseMediaAuto(cmd, sshTarget, sshKey, dir, stream, timeout)
		},
	}

	cmd.Flags().StringVar(&sshTarget, "ssh", "", "SSH target (user@host)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with edge templates")
	cmd.Flags().StringVar(&stream, "stream", "", "specific stream name to analyze")
	cmd.Flags().StringVar(&analyzer, "analyzer", "", "analyzer binary (HLS, RTMP, TS, H264, etc.)")
	cmd.Flags().StringVar(&target, "target", "", "URL or file path to analyze")
	cmd.Flags().IntVar(&detail, "detail", 2, "detail level 0-10 (0-5=text, 6-10=raw)")
	cmd.Flags().IntVar(&timeout, "timeout", 10, "timeout in seconds for live stream analysis")
	cmd.Flags().BoolVar(&validateOnly, "validate", true, "stop at first problem")

	return cmd
}

// runDiagnoseMediaWrapper handles the shorthand: frameworks edge diagnose media
func runDiagnoseMediaWrapper(cmd *cobra.Command, sshTarget, sshKey, dir string) error {
	return runDiagnoseMediaAuto(cmd, sshTarget, sshKey, dir, "", 10)
}

func runDiagnoseMediaAuto(cmd *cobra.Command, sshTarget, sshKey, dir, stream string, timeout int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner, cleanup, err := makeEdgeRunner(sshTarget, sshKey)
	if err != nil {
		return err
	}
	defer cleanup()

	mode := detectEdgeMode(dir, ".edge.env", sshTarget, sshKey)
	ar := mistdiag.NewAnalyzerRunner(runner, mode)

	// Show available analyzers
	available, err := ar.Available(ctx)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Could not list analyzers: %v\n", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "MistServer Media Diagnostics (%s mode)\n", mode)
	if len(available) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Available analyzers: %s\n", strings.Join(available, ", "))
	}

	// Discover active streams
	streams, err := mistdiag.DiscoverStreams(ctx, runner, mode)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "\n⚠ Could not discover streams: %v\n", err)
		fmt.Fprintln(cmd.OutOrStdout(), "  Hint: Is MistServer running? Check with 'frameworks edge status'")
		return nil
	}

	// Filter to specific stream if requested
	if stream != "" {
		var filtered []mistdiag.ActiveStream
		for _, s := range streams {
			if s.Name == stream {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("stream %q not found in active streams", stream)
		}
		streams = filtered
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Active streams: %d\n\n", len(streams))

	if len(streams) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active streams to analyze.")
		return nil
	}

	// Validate each stream via HLS
	passed := 0
	for _, s := range streams {
		sourceInfo := ""
		if s.Source != "" {
			sourceInfo = fmt.Sprintf(" (source: %s)", s.Source)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Stream: %s%s\n", s.Name, sourceInfo)

		result, err := ar.Validate(ctx, "HLS", s.HLSURL, timeout)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  HLS Validate:  ⚠ error: %v\n\n", err)
			continue
		}

		if result.OK {
			passed++
			fmt.Fprintf(cmd.OutOrStdout(), "  HLS Validate:  ✓ PASS\n")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  HLS Validate:  ✗ FAIL\n")
			if first := result.FirstError(); first != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    Error: %s\n", first)
			}
		}

		if len(result.Warnings) > 0 {
			for _, w := range result.Warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "    Warning: %s\n", w)
			}
		}

		fmt.Fprintln(cmd.OutOrStdout())
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Summary: %d/%d streams healthy\n", passed, len(streams))
	return nil
}

func runDiagnoseMediaManual(cmd *cobra.Command, sshTarget, sshKey, dir, analyzer, target string, detail, timeout int, validate bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout+30)*time.Second)
	defer cancel()

	runner, cleanup, err := makeEdgeRunner(sshTarget, sshKey)
	if err != nil {
		return err
	}
	defer cleanup()

	mode := detectEdgeMode(dir, ".edge.env", sshTarget, sshKey)
	ar := mistdiag.NewAnalyzerRunner(runner, mode)

	analyzer = mistdiag.NormalizeAnalyzerName(analyzer)
	fmt.Fprintf(cmd.OutOrStdout(), "Running MistAnalyser%s on %s (detail=%d, validate=%v)\n\n", analyzer, target, detail, validate)

	result, err := ar.Run(ctx, mistdiag.AnalyzerOptions{
		Analyzer: analyzer,
		Target:   target,
		Detail:   detail,
		Validate: validate,
		Timeout:  timeout,
	})
	if err != nil {
		return fmt.Errorf("analyzer failed: %w", err)
	}

	// In manual mode, stream raw output
	fmt.Fprint(cmd.OutOrStdout(), result.Output)
	if !strings.HasSuffix(result.Output, "\n") {
		fmt.Fprintln(cmd.OutOrStdout())
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("analyzer exited with code %d", result.ExitCode)
	}
	return nil
}

// makeEdgeRunner creates an ssh.Runner from command flags.
// Returns the runner, a cleanup function, and any error.
func makeEdgeRunner(sshTarget, sshKey string) (fwssh.Runner, func(), error) {
	sshTarget = strings.TrimSpace(sshTarget)
	if sshTarget == "" {
		return fwssh.NewLocalRunner(""), func() {}, nil
	}

	pool := fwssh.NewPool(30 * time.Second)
	cleanup := func() { pool.Close() }

	// Parse user@host
	user := "root"
	host := sshTarget
	if idx := strings.Index(sshTarget, "@"); idx >= 0 {
		user = sshTarget[:idx]
		host = sshTarget[idx+1:]
	}

	runner, err := pool.Get(&fwssh.ConnectionConfig{
		Address: host,
		Port:    22,
		User:    user,
		KeyPath: sshKey,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("SSH connection to %s failed: %w", sshTarget, err)
	}

	return runner, cleanup, nil
}
