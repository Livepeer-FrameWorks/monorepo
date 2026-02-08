package wireguard

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

func renderConfig(cfg Config) (string, error) {
	if err := validateConfig(cfg); err != nil {
		return "", err
	}

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
		return "", err
	}

	return buf.String(), nil
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.PrivateKey) == "" {
		return fmt.Errorf("private key is required")
	}
	if strings.TrimSpace(cfg.Address) == "" {
		return fmt.Errorf("address is required")
	}
	if cfg.ListenPort <= 0 {
		return fmt.Errorf("listen port must be positive")
	}

	for i, peer := range cfg.Peers {
		if strings.TrimSpace(peer.PublicKey) == "" {
			return fmt.Errorf("peer %d has empty public key", i)
		}
		if len(peer.AllowedIPs) == 0 {
			return fmt.Errorf("peer %d has no allowed IPs", i)
		}
		for _, ip := range peer.AllowedIPs {
			if strings.TrimSpace(ip) == "" {
				return fmt.Errorf("peer %d has empty allowed IP", i)
			}
		}
		if peer.KeepAlive < 0 {
			return fmt.Errorf("peer %d has negative keepalive", i)
		}
	}

	return nil
}
