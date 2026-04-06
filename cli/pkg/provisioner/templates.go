package provisioner

import (
	"fmt"
	"strings"
	"text/template"
)

// SystemdUnitData holds data for systemd unit template
type SystemdUnitData struct {
	ServiceName string
	Description string
	WorkingDir  string
	ExecStart   string
	User        string
	EnvFile     string
	After       []string // Service dependencies
	Restart     string
	RestartSec  string
	LimitNOFILE string // e.g., "1048576" for services needing high fd count
}

// LaunchdPlistData holds data for macOS launchd plist generation.
type LaunchdPlistData struct {
	Label       string            // e.g., "com.livepeer.frameworks.helmsman"
	Description string            // Human-readable service description
	Program     string            // Path to binary
	ProgramArgs []string          // Additional arguments
	WorkingDir  string            // WorkingDirectory
	EnvVars     map[string]string // Environment variables
	EnvFile     string            // Path to env file (loaded via wrapper script)
	RunAtLoad   bool
	KeepAlive   bool
	UserName    string
	LogPath     string // StandardOutPath
	ErrorPath   string // StandardErrorPath
}

// GenerateLaunchdPlist creates a macOS launchd plist file.
func GenerateLaunchdPlist(data LaunchdPlistData) (string, error) {
	const tmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Program}}</string>
{{- range .ProgramArgs}}
		<string>{{.}}</string>
{{- end}}
	</array>
	<key>WorkingDirectory</key>
	<string>{{.WorkingDir}}</string>
{{- if .EnvVars}}
	<key>EnvironmentVariables</key>
	<dict>
{{- range $k, $v := .EnvVars}}
		<key>{{$k}}</key>
		<string>{{$v}}</string>
{{- end}}
	</dict>
{{- end}}
	<key>RunAtLoad</key>
	<{{if .RunAtLoad}}true{{else}}false{{end}}/>
	<key>KeepAlive</key>
	<{{if .KeepAlive}}true{{else}}false{{end}}/>
{{- if .UserName}}
	<key>UserName</key>
	<string>{{.UserName}}</string>
{{- end}}
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.ErrorPath}}</string>
</dict>
</plist>
`

	if data.LogPath == "" {
		data.LogPath = "/usr/local/var/log/frameworks/" + data.Label + ".log"
	}
	if data.ErrorPath == "" {
		data.ErrorPath = "/usr/local/var/log/frameworks/" + data.Label + ".err"
	}

	t, err := template.New("launchd").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// DockerComposeData holds data for docker-compose template
type DockerComposeData struct {
	ServiceName string
	Image       string // With @sha256 digest
	Port        int
	Ports       []string // Additional ports
	EnvFile     string
	Environment map[string]string // Inline environment variables
	HealthCheck *HealthCheckConfig
	Networks    []string
	Volumes     []string
	ExtraHosts  []string // For host.docker.internal mapping
}

// HealthCheckConfig defines health check parameters
type HealthCheckConfig struct {
	Test     []string
	Interval string
	Timeout  string
	Retries  int
}

// CaddyfileData holds data for Caddyfile template
type CaddyfileData struct {
	Email         string
	RootDomain    string
	ListenAddress string
	Routes        []ProxyRoute
}

type ProxyRoute struct {
	Name          string
	ServerNames   []string
	Upstream      string
	UpgradeAll    bool
	WebsocketPath string
	GeoProxy      bool
}

// GenerateSystemdUnit creates a systemd unit file
func GenerateSystemdUnit(data SystemdUnitData) (string, error) {
	const tmpl = `[Unit]
Description={{.Description}}
After=network-online.target{{range .After}} {{.}}.service{{end}}
Wants=network-online.target

[Service]
Type=simple
User={{.User}}
Group={{.User}}
WorkingDirectory={{.WorkingDir}}

{{if .EnvFile}}EnvironmentFile={{.EnvFile}}{{end}}

ExecStart={{.ExecStart}}

Restart={{.Restart}}
RestartSec={{.RestartSec}}

StandardOutput=journal
StandardError=journal
SyslogIdentifier={{.ServiceName}}

{{if .LimitNOFILE}}LimitNOFILE={{.LimitNOFILE}}
{{end}}NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
`

	// Defaults
	if data.User == "" {
		data.User = "frameworks"
	}
	if data.Restart == "" {
		data.Restart = "always"
	}
	if data.RestartSec == "" {
		data.RestartSec = "5s"
	}

	t, err := template.New("systemd").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// GenerateDockerCompose creates a docker-compose.yml file
func GenerateDockerCompose(data DockerComposeData) (string, error) {
	const tmpl = `version: '3.8'

services:
  {{.ServiceName}}:
    image: {{.Image}}
    container_name: frameworks-{{.ServiceName}}
    restart: always

    {{if .Port}}ports:
      - "{{.Port}}:{{.Port}}"{{end}}
    {{if .Ports}}{{range .Ports}}
      - "{{.}}"{{end}}{{end}}

    {{if .EnvFile}}env_file:
      - {{.EnvFile}}{{end}}

    {{if .Environment}}environment:{{range $k, $v := .Environment}}
      {{$k}}: "{{$v}}"{{end}}{{end}}

    {{if .HealthCheck}}healthcheck:
      test: [{{range $i, $v := .HealthCheck.Test}}{{if $i}}, {{end}}"{{$v}}"{{end}}]
      interval: {{.HealthCheck.Interval}}
      timeout: {{.HealthCheck.Timeout}}
      retries: {{.HealthCheck.Retries}}
      start_period: 40s{{end}}

    {{if .Volumes}}volumes:{{range .Volumes}}
      - {{.}}{{end}}{{end}}

    {{if .Networks}}networks:{{range .Networks}}
      - {{.}}{{end}}{{end}}

    {{if .ExtraHosts}}extra_hosts:{{range .ExtraHosts}}
      - "{{.}}"{{end}}{{end}}

    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "10"
        labels: "service={{.ServiceName}}"

{{if .Networks}}networks:{{range .Networks}}
  {{.}}: 
    driver: bridge{{end}}{{end}}
`

	t, err := template.New("docker-compose").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// GenerateEnvFile creates an environment file
func GenerateEnvFile(serviceName string, config map[string]string) string {
	var lines []string

	lines = append(lines, fmt.Sprintf("# Environment for %s", serviceName))
	lines = append(lines, fmt.Sprintf("SERVICE_NAME=%s", serviceName))
	lines = append(lines, "")

	for key, value := range config {
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
	}

	return strings.Join(lines, "\n")
}

// GenerateCentralCaddyfile creates the Caddyfile dynamically based on present services
func GenerateCentralCaddyfile(data CaddyfileData) (string, error) {
	if data.RootDomain == "" {
		return "", fmt.Errorf("RootDomain is required for Caddyfile generation")
	}
	if data.ListenAddress == "" {
		data.ListenAddress = ":80"
	}

	const tmpl = `{ 
{{- if .Email }}
	email {{.Email}}
{{- end }}
}

# Health Check (always available)
{{.ListenAddress}} {
	handle /health {
		respond "healthy" 200
	}
}

{{range .Routes}}
{{join .ServerNames ", "}} {
	handle {
		reverse_proxy {{.Upstream}}
	}
}
{{end}}
`
	t, err := template.New("caddyfile").Funcs(template.FuncMap{
		"join": strings.Join,
	}).Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// NginxConfData holds data for nginx.conf template
type NginxConfData struct {
	RootDomain    string
	ListenAddress string // e.g. "80"
	GeoIPDBPath   string
	Routes        []ProxyRoute
}

// GenerateNginxConf creates an nginx config with subdomain-based routing
func GenerateNginxConf(data NginxConfData) (string, error) {
	if data.RootDomain == "" {
		return "", fmt.Errorf("RootDomain is required for nginx config generation")
	}
	if data.ListenAddress == "" {
		data.ListenAddress = "80"
	}

	const tmpl = `server {
    listen 127.0.0.1:18090;
    server_name frameworks-health.local;

    location /health {
        access_log off;
        return 200 "healthy\n";
        add_header Content-Type text/plain;
    }

    location /nginx_status {
        stub_status on;
        access_log off;
        allow 127.0.0.1;
        deny all;
    }
}
{{if .GeoIPDBPath}}
geoip2 {{.GeoIPDBPath}} {
    $geo_lat location latitude;
    $geo_lon location longitude;
}
{{end}}
{{range .Routes}}
server {
    listen {{.ListenAddress}};
    server_name {{join .ServerNames " "}};
{{if .WebsocketPath}}
    location {{.WebsocketPath}} {
        proxy_pass http://{{.Upstream}}{{.WebsocketPath}};
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 86400;
    }
{{end}}
    location / {
        proxy_pass http://{{.Upstream}};
{{if .UpgradeAll}}
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
{{end}}
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
{{if .GeoProxy}}
        proxy_set_header X-Latitude $geo_lat;
        proxy_set_header X-Longitude $geo_lon;
{{end}}
    }
}
{{end}}`

	t, err := template.New("nginx").Funcs(template.FuncMap{
		"join": strings.Join,
	}).Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func BuildLocalProxyRoutes(rootDomain string, localServices map[string]int) []ProxyRoute {
	if rootDomain == "" || len(localServices) == 0 {
		return nil
	}

	order := []string{"foredeck", "bridge", "chartroom", "logbook", "steward", "listmonk", "chatwoot", "foghorn"}
	routes := make([]ProxyRoute, 0, len(localServices))
	for _, name := range order {
		port, ok := localServices[name]
		if !ok || port == 0 {
			continue
		}

		route := ProxyRoute{
			Name:     name,
			Upstream: fmt.Sprintf("127.0.0.1:%d", port),
		}

		switch name {
		case "foredeck":
			route.ServerNames = []string{rootDomain, "www." + rootDomain}
		default:
			route.ServerNames = []string{name + "." + rootDomain}
		}

		switch name {
		case "bridge":
			route.WebsocketPath = "/graphql/ws"
		case "chartroom":
			route.UpgradeAll = true
		case "chatwoot":
			route.WebsocketPath = "/cable"
		case "foghorn":
			route.GeoProxy = true
		}

		routes = append(routes, route)
	}

	return routes
}

func BuildExtraProxyRoutes(raw interface{}) []ProxyRoute {
	items, ok := raw.([]map[string]interface{})
	if !ok || len(items) == 0 {
		list, ok := raw.([]interface{})
		if !ok || len(list) == 0 {
			return nil
		}
		items = make([]map[string]interface{}, 0, len(list))
		for _, item := range list {
			entry, ok := item.(map[string]interface{})
			if ok {
				items = append(items, entry)
			}
		}
		if len(items) == 0 {
			return nil
		}
	}

	routes := make([]ProxyRoute, 0, len(items))
	for _, item := range items {
		name, _ := item["name"].(string)
		upstream, _ := item["upstream"].(string)
		serverNames := stringifySlice(item["server_names"])
		if upstream == "" || len(serverNames) == 0 {
			continue
		}
		route := ProxyRoute{
			Name:        name,
			ServerNames: serverNames,
			Upstream:    upstream,
		}
		if websocketPath, _ := item["websocket_path"].(string); websocketPath != "" {
			route.WebsocketPath = websocketPath
		}
		if geoProxy, _ := item["geo_proxy"].(bool); geoProxy {
			route.GeoProxy = true
		}
		if upgradeAll, _ := item["upgrade_all"].(bool); upgradeAll {
			route.UpgradeAll = true
		}
		routes = append(routes, route)
	}

	return routes
}

func stringifySlice(raw interface{}) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
