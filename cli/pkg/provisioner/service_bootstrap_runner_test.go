package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// These tests pin the runner's input-validation contract. They never reach
// the ansible-playbook invocation — every case returns before that — so they
// run without a target host.

func TestRunServiceBootstrap_RejectsEmptyService(t *testing.T) {
	pool := ssh.NewPool(0, "")
	defer pool.Close()
	err := RunServiceBootstrap(context.Background(), pool, ServiceBootstrapOptions{
		Service: "",
		Host:    inventory.Host{Name: "h", ExternalIP: "127.0.0.1"},
		YAML:    "x",
	})
	if err == nil || !strings.Contains(err.Error(), "Service required") {
		t.Fatalf("expected Service-required error, got %v", err)
	}
}

func TestRunServiceBootstrap_ApplyRequiresYAML(t *testing.T) {
	pool := ssh.NewPool(0, "")
	defer pool.Close()
	err := RunServiceBootstrap(context.Background(), pool, ServiceBootstrapOptions{
		Service: "purser",
		Host:    inventory.Host{Name: "h", ExternalIP: "127.0.0.1"},
		Mode:    ServiceBootstrapModeApply,
	})
	if err == nil || !strings.Contains(err.Error(), "YAML required") {
		t.Fatalf("expected YAML-required error for apply mode, got %v", err)
	}
}

func TestRunServiceBootstrap_ValidateAcceptsEmptyYAML(t *testing.T) {
	pool := ssh.NewPool(0, "")
	defer pool.Close()
	// Validate must NOT require YAML (it's a post-state DB+gRPC check); the
	// runner advances past argument validation. The actual ansible call
	// fails because the test environment has no inventory/ssh, so we accept
	// any error EXCEPT a "YAML required" one.
	err := RunServiceBootstrap(context.Background(), pool, ServiceBootstrapOptions{
		Service: "purser",
		Host:    inventory.Host{Name: "h", ExternalIP: "127.0.0.1"},
		Mode:    ServiceBootstrapModeValidate,
	})
	if err != nil && strings.Contains(err.Error(), "YAML required") {
		t.Fatalf("validate mode should not require YAML, got %v", err)
	}
}

func TestRunServiceBootstrap_RejectsUnknownMode(t *testing.T) {
	pool := ssh.NewPool(0, "")
	defer pool.Close()
	err := RunServiceBootstrap(context.Background(), pool, ServiceBootstrapOptions{
		Service: "purser",
		Host:    inventory.Host{Name: "h", ExternalIP: "127.0.0.1"},
		Mode:    "destroy",
		YAML:    "x",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("expected unknown-mode error, got %v", err)
	}
}

func TestRunServiceBootstrap_RejectsHostWithoutAddress(t *testing.T) {
	pool := ssh.NewPool(0, "")
	defer pool.Close()
	err := RunServiceBootstrap(context.Background(), pool, ServiceBootstrapOptions{
		Service: "purser",
		Host:    inventory.Host{},
		YAML:    "x",
	})
	if err == nil || !strings.Contains(err.Error(), "ExternalIP or Name") {
		t.Fatalf("expected host-address error, got %v", err)
	}
}
