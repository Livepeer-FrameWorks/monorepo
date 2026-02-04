package wireguard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
)

type linuxManager struct {
	interfaceName string
	configPath    string
}

func newLinuxManager(interfaceName string) Manager {
	return &linuxManager{
		interfaceName: interfaceName,
		configPath:    "/etc/wireguard", // Standard location
	}
}

func (m *linuxManager) Init() error {
	// Check if interface exists
	_, err := exec.Command("ip", "link", "show", m.interfaceName).Output()
	if err != nil {
		// Create interface
		if out, err := exec.Command("ip", "link", "add", "dev", m.interfaceName, "type", "wireguard").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create interface: %w: %s", err, string(out))
		}
	}

	// Ensure it is up
	if out, err := exec.Command("ip", "link", "set", "up", "dev", m.interfaceName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set interface up: %w: %s", err, string(out))
	}

	return nil
}

func (m *linuxManager) GetPublicKey() (string, error) {
	// Ensure directory exists
	if err := os.MkdirAll(m.configPath, 0700); err != nil {
		return "", err
	}

	keyPath := fmt.Sprintf("%s/%s.key", m.configPath, m.interfaceName)

	// Check if private key exists
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		// Generate private key
		out, err := exec.Command("wg", "genkey").Output()
		if err != nil {
			return "", fmt.Errorf("failed to generate private key: %w", err)
		}
		privKey := strings.TrimSpace(string(out))
		if err := os.WriteFile(keyPath, []byte(privKey), 0600); err != nil {
			return "", err
		}
	}

	// Read private key
	privKeyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}

	// Generate public key
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = bytes.NewReader(privKeyBytes)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to generate public key: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

func (m *linuxManager) GetPrivateKey() (string, error) {
	keyPath := fmt.Sprintf("%s/%s.key", m.configPath, m.interfaceName)
	privKeyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(privKeyBytes)), nil
}

func (m *linuxManager) Apply(cfg Config) error {
	// 1. Write config to temp file
	tmpl := `[Interface]
PrivateKey = {{.PrivateKey}}
ListenPort = {{.ListenPort}}

{{range .Peers}}
[Peer]
PublicKey = {{.PublicKey}}
Endpoint = {{.Endpoint}}
AllowedIPs = {{range $i, $ip := .AllowedIPs}}{{if $i}}, {{end}}{{$ip}}{{end}}
PersistentKeepalive = {{.KeepAlive}}
{{end}}
`
	t := template.Must(template.New("wg-config").Parse(tmpl))
	var buf bytes.Buffer
	if err := t.Execute(&buf, cfg); err != nil {
		return err
	}

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
	if out, err := exec.Command("wg", "setconf", m.interfaceName, tmpFile.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply wireguard config: %w: %s", err, string(out))
	}

	// 3. Set IP Address
	// Check current IP
	// This is a bit naive (assumes single IP), but sufficient for mesh
	currentIPs, err := exec.Command("ip", "-o", "-4", "addr", "show", m.interfaceName).Output()
	if err == nil {
		if !strings.Contains(string(currentIPs), cfg.Address) {
			// Flush old IPs (best-effort, continue even if fails)
			_ = exec.CommandContext(context.Background(), "ip", "addr", "flush", "dev", m.interfaceName).Run() //nolint:errcheck // best-effort flush before adding new IP
			// Add new IP
			if out, err := exec.Command("ip", "addr", "add", cfg.Address, "dev", m.interfaceName).CombinedOutput(); err != nil {
				return fmt.Errorf("failed to set ip address: %w: %s", err, string(out))
			}
		}
	}

	return nil
}

func (m *linuxManager) Close() error {
	return nil
}
