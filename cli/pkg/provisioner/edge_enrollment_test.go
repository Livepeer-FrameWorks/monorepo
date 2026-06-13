package provisioner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/ssh"
)

// runEnrollmentProbe executes the real probe script through /bin/sh against a
// candidate list, returning trimmed stdout and the exit code. Exercises the
// shell semantics the classifier table can't (the script is what decides
// fresh vs. unreadable). The privileged sudo branch is not covered here — it
// can't be deterministically reproduced across root/non-root CI — only the
// readable-file and no-marker branches, which carry the fresh-host logic.
func runEnrollmentProbe(t *testing.T, candidates []string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sh", "-c", edgeEnrollmentProbeScript(candidates))
	out, err := cmd.Output()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else {
			t.Fatalf("probe run failed: %v", err)
		}
	}
	return strings.TrimSpace(string(out)), exit
}

func TestEnrollmentProbeScript(t *testing.T) {
	dir := t.TempDir()
	enrolled := filepath.Join(dir, "enrolled.env")
	if err := os.WriteFile(enrolled, []byte("NODE_ID=n1\nMIST_API_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank.env")
	if err := os.WriteFile(blank, []byte("MIST_API_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.env")

	t.Run("identity from readable marker", func(t *testing.T) {
		out, exit := runEnrollmentProbe(t, []string{missing, enrolled})
		if exit != 0 || !strings.Contains(out, "NODE_ID=n1") {
			t.Fatalf("exit=%d out=%q, want exit 0 with NODE_ID", exit, out)
		}
	})
	t.Run("readable marker without identity keys is fresh", func(t *testing.T) {
		out, exit := runEnrollmentProbe(t, []string{blank})
		if exit != 0 || out != "" {
			t.Fatalf("exit=%d out=%q, want exit 0 empty (fresh)", exit, out)
		}
	})
	t.Run("no marker file is fresh", func(t *testing.T) {
		_, exit := runEnrollmentProbe(t, []string{missing})
		if exit != 3 {
			t.Fatalf("exit=%d, want 3 (fresh, no marker)", exit)
		}
	})
}

// The runners return a non-nil error for ANY non-zero exit, so the probe's
// deliberate exit codes must be classified from result.ExitCode with the
// error present — a fresh host (exit 3) is a state, not a failure.
func TestClassifyEnrollmentProbe(t *testing.T) {
	exitErr := errors.New("ssh: command exited with 3")
	cases := []struct {
		name       string
		result     *ssh.CommandResult
		runErr     error
		wantErr    string // substring; empty = no error
		wantNodeID string
		wantFresh  bool
	}{
		{
			name:       "enrolled identity on exit 0",
			result:     &ssh.CommandResult{ExitCode: 0, Stdout: "NODE_ID=n1\nCLUSTER_ID=c1"},
			wantNodeID: "n1",
		},
		{
			name:      "exit 0 with empty output is fresh",
			result:    &ssh.CommandResult{ExitCode: 0},
			wantFresh: true,
		},
		{
			name:      "fresh host exit 3 despite runner error",
			result:    &ssh.CommandResult{ExitCode: 3},
			runErr:    exitErr,
			wantFresh: true,
		},
		{
			name:    "unreadable marker exit 4 is an error",
			result:  &ssh.CommandResult{ExitCode: 4},
			runErr:  exitErr,
			wantErr: "could not be read",
		},
		{
			name:    "runner process failure",
			result:  &ssh.CommandResult{ExitCode: -1},
			runErr:  errors.New("ssh: spawn failed"),
			wantErr: "probe failed",
		},
		{
			name:    "ssh transport failure exit 255",
			result:  &ssh.CommandResult{ExitCode: 255, Stderr: "connection refused"},
			runErr:  exitErr,
			wantErr: "connection refused",
		},
		{
			name:    "nil result",
			runErr:  errors.New("no runner"),
			wantErr: "no runner",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enrollment, err := classifyEnrollmentProbe(tc.result, tc.runErr, "ubuntu")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantFresh {
				if enrollment.Enrolled() {
					t.Fatalf("expected fresh host, got %+v", enrollment)
				}
				return
			}
			if enrollment.NodeID != tc.wantNodeID {
				t.Fatalf("NodeID = %q, want %q", enrollment.NodeID, tc.wantNodeID)
			}
		})
	}
}

func TestParseEdgeEnrollmentEnv(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		want     EdgeEnrollment
		enrolled bool
	}{
		{
			name: "full identity",
			content: "NODE_ID=edge-abc123\n" +
				"EDGE_DOMAIN=edge-abc123.media-eu.example.com\n" +
				"FOGHORN_CONTROL_ADDR=foghorn.media-eu.example.com:18029\n" +
				"CLUSTER_ID=media-eu\n",
			want: EdgeEnrollment{
				NodeID:      "edge-abc123",
				EdgeDomain:  "edge-abc123.media-eu.example.com",
				FoghornAddr: "foghorn.media-eu.example.com:18029",
				ClusterID:   "media-eu",
			},
			enrolled: true,
		},
		{
			name:     "whitespace and unrelated lines",
			content:  "  NODE_ID=n1  \nGARBAGE\nMIST_API_PASSWORD=secret\n",
			want:     EdgeEnrollment{NodeID: "n1"},
			enrolled: true,
		},
		{
			name:     "empty node id is not enrolled",
			content:  "NODE_ID=\nCLUSTER_ID=c1\n",
			want:     EdgeEnrollment{ClusterID: "c1"},
			enrolled: false,
		},
		{
			name:     "empty content",
			content:  "",
			want:     EdgeEnrollment{},
			enrolled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseEdgeEnrollmentEnv(tc.content)
			if *got != tc.want {
				t.Fatalf("parseEdgeEnrollmentEnv = %+v, want %+v", *got, tc.want)
			}
			if got.Enrolled() != tc.enrolled {
				t.Fatalf("Enrolled() = %v, want %v", got.Enrolled(), tc.enrolled)
			}
		})
	}
}

// Already-enrolled nodes don't need a token (Foghorn resolves them by
// fingerprint); a forced re-enroll does.
func TestRequireEnrollmentTokenAlreadyEnrolled(t *testing.T) {
	cases := []struct {
		name    string
		cfg     EdgeProvisionConfig
		wantErr bool
	}{
		{name: "fresh node without token", cfg: EdgeProvisionConfig{}, wantErr: true},
		{name: "fresh node with token", cfg: EdgeProvisionConfig{EnrollmentToken: "tok"}, wantErr: false},
		{name: "enrolled node without token", cfg: EdgeProvisionConfig{AlreadyEnrolled: true}, wantErr: false},
		{name: "forced re-enroll without token", cfg: EdgeProvisionConfig{AlreadyEnrolled: true, ForceReenroll: true}, wantErr: true},
		{name: "forced re-enroll with token", cfg: EdgeProvisionConfig{AlreadyEnrolled: true, ForceReenroll: true, EnrollmentToken: "tok"}, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.requireEnrollmentToken()
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireEnrollmentToken() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
