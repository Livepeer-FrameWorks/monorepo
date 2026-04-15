package system

import (
	"bytes"
	"fmt"
	"text/template"
)

// SystemdResolvedConfig represents the configuration for systemd-resolved
type SystemdResolvedConfig struct {
	Port int
}

// GenerateSystemdResolvedConfig generates the configuration content for systemd-resolved
func GenerateSystemdResolvedConfig(port int) (string, error) {
	const tmpl = `[Resolve]
DNS=127.0.0.1:{{.Port}}
Domains=~internal
DNSStubListener=yes
`
	t, err := template.New("resolved").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, SystemdResolvedConfig{Port: port}); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// DetectSystemdResolved checks if systemd-resolved is active on the host
// This is a shell script to be executed remotely
func DetectSystemdResolved() string {
	return `systemctl is-active systemd-resolved`
}

// ConfigureSystemdResolved returns a script to configure systemd-resolved
func ConfigureSystemdResolved(configContent string) string {
	return fmt.Sprintf(`#!/bin/bash
set -e

# Create configuration directory
mkdir -p /etc/systemd/resolved.conf.d

# Write configuration
cat <<EOF > /etc/systemd/resolved.conf.d/frameworks-privateer.conf
%s
EOF

# Restart systemd-resolved
systemctl restart systemd-resolved

# Check status
if systemctl is-active --quiet systemd-resolved; then
    echo "systemd-resolved configured and restarted"
else
    echo "Failed to restart systemd-resolved"
    exit 1
fi
`, configContent)
}

// CaptureUpstreamNameservers returns a script that prints the current nameserver
// entries from /etc/resolv.conf (excluding loopback) as a comma-separated list.
// Run this BEFORE overwriting resolv.conf so Privateer can forward non-.internal queries.
func CaptureUpstreamNameservers() string {
	return `awk '/^nameserver/ && $2 !~ /^127\./ {printf sep $2; sep=","}' /etc/resolv.conf`
}

// ConfigureResolvConf returns a script to configure resolv.conf directly (fallback)
func ConfigureResolvConf() string {
	return `#!/bin/bash
# Fallback: direct modification of /etc/resolv.conf
# This is less robust as it might be overwritten by other tools

if grep -q "nameserver 127.0.0.1" /etc/resolv.conf; then
    echo "resolv.conf already configured"
    exit 0
fi

# Prepend localhost to nameservers for .internal routing
sed -i '1s/^/nameserver 127.0.0.1\noptions ndots:1\n/' /etc/resolv.conf
echo "Modified /etc/resolv.conf"
`
}
