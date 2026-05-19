package provisioner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func ansiblePythonVars(ctx context.Context, pool *ssh.Pool, host inventory.Host, address, connection string) map[string]any {
	path, _ := probeAnsiblePython(ctx, pool, host, address, connection)
	if path == "" {
		return nil
	}
	return map[string]any{"ansible_python_interpreter": path}
}

func ensureRemoteAnsiblePython(ctx context.Context, pool *ssh.Pool, host inventory.Host, dryRun bool) error {
	address := hostAddressFor(host)
	connection := ""
	if address == "localhost" || address == "127.0.0.1" {
		connection = "local"
	}
	path, err := probeAnsiblePython(ctx, pool, host, address, connection)
	if err != nil {
		return err
	}
	if path != "" {
		fmt.Printf("  ansible python: %s\n", path)
		return nil
	}
	if dryRun {
		return fmt.Errorf("remote host %s has no Python interpreter for Ansible; install python first or run non-dry provisioning to bootstrap it", address)
	}
	if pool == nil {
		return fmt.Errorf("remote host %s has no Python interpreter for Ansible and no SSH pool is available to bootstrap it", address)
	}
	fmt.Printf("  ansible python: missing on %s, bootstrapping with remote package manager\n", address)
	installCmd := strings.Join([]string{
		`set -e`,
		`if [ "$(id -u)" -eq 0 ]; then SUDO=""; elif command -v sudo >/dev/null 2>&1; then SUDO="sudo"; else echo "python missing and sudo unavailable" >&2; exit 1; fi`,
		`if command -v pacman >/dev/null 2>&1; then $SUDO pacman -Sy --noconfirm python`,
		`elif command -v apt-get >/dev/null 2>&1; then $SUDO env DEBIAN_FRONTEND=noninteractive apt-get update && $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y python3`,
		`elif command -v dnf >/dev/null 2>&1; then $SUDO dnf install -y python3`,
		`elif command -v yum >/dev/null 2>&1; then $SUDO yum install -y python3`,
		`elif command -v apk >/dev/null 2>&1; then $SUDO apk add --no-cache python3`,
		`else echo "python missing and no supported package manager found" >&2; exit 1; fi`,
	}, "; ")
	result, err := pool.Run(ctx, ansibleSSHConfig(host, address), installCmd)
	if err != nil {
		return fmt.Errorf("bootstrap Python for Ansible on %s: %w", address, err)
	}
	if result != nil && result.ExitCode != 0 {
		return fmt.Errorf("bootstrap Python for Ansible on %s exited %d: %s", address, result.ExitCode, result.Stderr)
	}
	path, err = probeAnsiblePython(ctx, pool, host, address, connection)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("bootstrapped Python on %s but no python interpreter was found afterward", address)
	}
	fmt.Printf("  ansible python: %s\n", path)
	return nil
}

func probeAnsiblePython(ctx context.Context, pool *ssh.Pool, host inventory.Host, address, connection string) (string, error) {
	if connection == "local" || address == "" || pool == nil {
		return "", nil
	}
	result, err := pool.Run(ctx, ansibleSSHConfig(host, address), "command -v python3 || command -v python || command -v python2")
	if err != nil || result == nil || result.ExitCode != 0 {
		return "", nil
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		path := strings.TrimSpace(line)
		if path != "" {
			return path, nil
		}
	}
	return "", nil
}

func ansibleSSHConfig(host inventory.Host, address string) *ssh.ConnectionConfig {
	return &ssh.ConnectionConfig{
		Address:  address,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  10 * time.Second,
	}
}
