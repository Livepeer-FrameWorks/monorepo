package wireguard

import (
	"bytes"
	"text/template"
)

// renderConfig renders a wg-quick / wg setconf style ini into a string.
// wgtypes.Key, *net.UDPAddr, and net.IPNet are all Stringer, so the
// template prints them via their canonical text representations.
func renderConfig(cfg Config) (string, error) {
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
