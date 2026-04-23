package provisioner

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/inventory"
)

// runValidatePlaybook executes a validate playbook for serviceName on host.
// The tasks use standard Ansible modules (uri, wait_for, command) so
// readiness and reachability live in the playbook, not in a bespoke Go
// probe framework. Gather_facts is off — Validate doesn't need them.
func runValidatePlaybook(
	ctx context.Context,
	executor *ansible.Executor,
	sshKeyPath string,
	host inventory.Host,
	serviceName string,
	tasks []ansible.Task,
) error {
	playbook := &ansible.Playbook{
		Name:  "validate: " + serviceName,
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "validate " + serviceName,
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: false,
				Tasks:       tasks,
			},
		},
	}
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": sshKeyPath,
		},
	})
	result, execErr := executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: false})
	if execErr != nil {
		return fmt.Errorf("validate %s failed: %w\nOutput: %s", serviceName, execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("validate %s playbook failed\nOutput: %s", serviceName, result.Output)
	}
	return nil
}

// uriOK emits ansible.builtin.uri for an HTTP health check. status is the
// expected code (e.g. 200); the task fails if the response doesn't match.
func uriOK(name, url string, status int) ansible.Task {
	return ansible.Task{
		Name:   name,
		Module: "ansible.builtin.uri",
		Args: map[string]any{
			"url":         url,
			"status_code": status,
			"timeout":     5,
		},
	}
}

// waitForTCP emits ansible.builtin.wait_for against host:port for a bound
// listener. Handles all the 0.0.0.0 / [::] wildcard matching that a
// hand-rolled ss regex would miss.
func waitForTCP(name, probeHost string, port, timeout int) ansible.Task {
	if timeout <= 0 {
		timeout = 10
	}
	return ansible.Task{
		Name:   name,
		Module: "ansible.builtin.wait_for",
		Args: map[string]any{
			"host":    probeHost,
			"port":    port,
			"state":   "started",
			"timeout": timeout,
		},
	}
}

// commandOK emits ansible.builtin.command with changed_when: false so it
// behaves as a pure validator (no idempotence drift). cmd is the full
// argv list; use a shell-ish `bash -lc` string when quoting gets in the way.
func commandOK(name string, argv ...string) ansible.Task {
	return ansible.Task{
		Name:        name,
		Module:      "ansible.builtin.command",
		Args:        map[string]any{"argv": argv},
		ChangedWhen: "false",
	}
}

// shellValidate emits ansible.builtin.shell for a probe that needs a pipe
// or redirection (zk ruok, kafka-topics with pipelining). changed_when:
// false keeps it validator-pure.
func shellValidate(name, cmd string) ansible.Task {
	return ansible.Task{
		Name:        name,
		Module:      "ansible.builtin.shell",
		Args:        map[string]any{"cmd": cmd},
		ChangedWhen: "false",
	}
}
