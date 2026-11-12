package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"github.com/spf13/cobra"
)

func statusFixture(ctx context.Context) (*cobra.Command, *resolvedCluster) {
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	return cmd, &resolvedCluster{Manifest: &inventory.Manifest{
		Hosts:    map[string]inventory.Host{"one": {ExternalIP: "127.0.0.1"}},
		Services: map[string]inventory.ServiceConfig{"bridge": {Enabled: true, Host: "one", Mode: "native"}},
	}}
}

func TestClusterStatusReturnsOperationalExitCodes(t *testing.T) {
	for _, test := range []struct {
		name  string
		state *detect.ServiceState
		err   error
		code  int
	}{
		{"healthy", &detect.ServiceState{Exists: true, Running: true, Version: "v0.3.14"}, nil, 0},
		{"stopped", &detect.ServiceState{Exists: true}, nil, 1},
		{"missing", nil, nil, 1},
		{"unreadable", nil, errors.New("SSH failed"), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, rc := statusFixture(t.Context())
			err := runClusterStatusWithRelease(cmd, rc, true, &gitops.Manifest{}, func(context.Context, inventory.Host, string) (*detect.ServiceState, error) {
				return test.state, test.err
			})
			code := 0
			var exit *ExitCodeError
			if errors.As(err, &exit) {
				code = exit.Code
			} else if err != nil {
				t.Fatal(err)
			}
			if code != test.code {
				t.Fatalf("exit=%d, want %d", code, test.code)
			}
		})
	}
}

func TestClusterStatusPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd, rc := statusFixture(ctx)
	err := runClusterStatusWithRelease(cmd, rc, true, &gitops.Manifest{}, func(ctx context.Context, _ inventory.Host, _ string) (*detect.ServiceState, error) {
		if ctx.Err() == nil {
			t.Error("inspection detached from command cancellation")
		}
		return nil, context.Canceled
	})
	var exit *ExitCodeError
	if !errors.As(err, &exit) || exit.Code != 130 {
		t.Fatalf("interrupt result=%v", err)
	}
}

func TestClusterStatusSlowFirstServiceDoesNotStarveLaterService(t *testing.T) {
	cmd, rc := statusFixture(t.Context())
	rc.Manifest.Services["purser"] = inventory.ServiceConfig{Enabled: true, Host: "one", Mode: "native"}
	later := make(chan struct{})
	err := runClusterStatusWithRelease(cmd, rc, true, &gitops.Manifest{}, func(ctx context.Context, _ inventory.Host, service string) (*detect.ServiceState, error) {
		if service == "bridge" {
			select {
			case <-later:
			case <-time.After(500 * time.Millisecond):
				t.Error("later service queued behind slow first service")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			close(later)
		}
		return &detect.ServiceState{Exists: true, Running: true, Version: "v0.3.14"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
