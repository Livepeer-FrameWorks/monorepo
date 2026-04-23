package wireguard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type linuxManager struct {
	interfaceName string
}

func newLinuxManager(interfaceName string) Manager {
	return &linuxManager{
		interfaceName: interfaceName,
	}
}

func (m *linuxManager) Init() error {
	ctx := context.Background()
	// Check if interface exists
	_, err := exec.CommandContext(ctx, "ip", "link", "show", m.interfaceName).Output()
	if err != nil {
		// Create interface
		if out, err := exec.CommandContext(ctx, "ip", "link", "add", "dev", m.interfaceName, "type", "wireguard").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create interface: %w: %s", err, string(out))
		}
	}

	// Ensure it is up
	if out, err := exec.CommandContext(ctx, "ip", "link", "set", "up", "dev", m.interfaceName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set interface up: %w: %s", err, string(out))
	}

	return nil
}

func (m *linuxManager) Apply(cfg Config) error {
	ctx := context.Background()
	// 1. Write config to temp file
	configText, err := renderConfig(cfg)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(configText)

	tmpFile, err := os.CreateTemp("", "wg-conf-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, writeErr := tmpFile.Write(buf.Bytes()); writeErr != nil {
		return writeErr
	}
	if closeErr := tmpFile.Close(); closeErr != nil {
		return closeErr
	}

	// 2. Apply with wg setconf
	// setconf replaces the current configuration
	if out, cmdErr := exec.CommandContext(ctx, "wg", "setconf", m.interfaceName, tmpFile.Name()).CombinedOutput(); cmdErr != nil {
		return fmt.Errorf("failed to apply wireguard config: %w: %s", cmdErr, string(out))
	}

	// 3. Set IP Address
	// Check current IP
	// This is a bit naive (assumes single IP), but sufficient for mesh
	currentIPs, err := exec.CommandContext(ctx, "ip", "-o", "-4", "addr", "show", m.interfaceName).Output()
	if err == nil {
		if !strings.Contains(string(currentIPs), cfg.Address) {
			// Flush old IPs (best-effort, continue even if fails)
			_ = exec.CommandContext(ctx, "ip", "addr", "flush", "dev", m.interfaceName).Run() //nolint:errcheck // best-effort flush before adding new IP
			// Add new IP
			if out, addErr := exec.CommandContext(ctx, "ip", "addr", "add", cfg.Address, "dev", m.interfaceName).CombinedOutput(); addErr != nil {
				return fmt.Errorf("failed to set ip address: %w: %s", addErr, string(out))
			}
		}
	}

	return nil
}

func (m *linuxManager) Close() error {
	return nil
}
