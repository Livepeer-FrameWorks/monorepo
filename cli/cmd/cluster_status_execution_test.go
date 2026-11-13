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
			for _, jsonOutput := range []bool{true, false} {
				cmd, rc := statusFixture(t.Context())
				err := runClusterStatusWithRelease(cmd, rc, jsonOutput, &gitops.Manifest{}, func(context.Context, inventory.Host, string) (*detect.ServiceState, error) {
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
			}
		})
	}
}

func TestClusterStatusPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd, rc := statusFixture(ctx)
	started := make(chan struct{})
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runClusterStatusWithRelease(cmd, rc, true, &gitops.Manifest{}, func(ctx context.Context, _ inventory.Host, _ string) (*detect.ServiceState, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil, ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("inspection never started")
	}
	cancel()
	select {
	case err := <-result:
		var exit *ExitCodeError
		if !errors.As(err, &exit) || exit.Code != 130 {
			t.Fatalf("interrupt result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cluster status did not return after cancellation")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("inspection did not observe command cancellation")
	}
}

func TestClusterStatusInspectionTimeoutIsIncomplete(t *testing.T) {
	for _, jsonOutput := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		cmd, rc := statusFixture(ctx)
		called := false
		err := runClusterStatusWithTimeout(cmd, rc, jsonOutput, &gitops.Manifest{}, func(ctx context.Context, _ inventory.Host, _ string) (*detect.ServiceState, error) {
			called = true
			<-ctx.Done()
			return nil, ctx.Err()
		}, 10*time.Millisecond)
		var exit *ExitCodeError
		if !called || !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("json=%v called=%v timeout=%v; want incomplete exit 2", jsonOutput, called, err)
		}
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

func TestClusterStatusUnverifiedDependencyIsIncompleteInBothFormats(t *testing.T) {
	for _, jsonOutput := range []bool{true, false} {
		cmd, rc := statusFixture(t.Context())
		rc.Manifest.Services = map[string]inventory.ServiceConfig{"gateway": {Enabled: true, Host: "one", Mode: "native", Deploy: "livepeer-gateway"}}
		manifest := &gitops.Manifest{ExternalDependencies: []gitops.ExternalDependency{*goLivepeerPin("v0.3.5")}}
		err := runClusterStatusWithRelease(cmd, rc, jsonOutput, manifest, func(context.Context, inventory.Host, string) (*detect.ServiceState, error) {
			return installedState("", nil), nil
		})
		var exit *ExitCodeError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("json=%v unverified dependency result=%v want incomplete exit 2", jsonOutput, err)
		}
	}
}
