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
	// Routes maps service names to their local ports.
	// Only services present in this map will have routes generated.
	Routes map[string]int
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
	email {$CADDY_EMAIL}
}

# Health Check (always available)
{{.ListenAddress}} {
	handle /health {
		respond "healthy" 200
	}
}

{{if .Routes.website}}
# Marketing Website (Root & www)
{$CADDY_ROOT_DOMAIN}, www.{$CADDY_ROOT_DOMAIN} {
	handle {
		reverse_proxy localhost:{{.Routes.website}}
	}
}
{{end}}

{{if .Routes.bridge}}
# GraphQL API Gateway & Auth
bridge.{$CADDY_ROOT_DOMAIN} {
	handle {
		reverse_proxy localhost:{{.Routes.bridge}}
	}
}
{{end}}

{{if .Routes.chartroom}}
# Web Application (Dashboard)
chartroom.{$CADDY_ROOT_DOMAIN} {
	handle {
		reverse_proxy localhost:{{.Routes.chartroom}}
	}
}
{{end}}

{{if .Routes.logbook}}
# Documentation
logbook.{$CADDY_ROOT_DOMAIN} {
	handle {
		reverse_proxy localhost:{{.Routes.logbook}}
	}
}
{{end}}

{{if .Routes.steward}}
# Forms Service
steward.{$CADDY_ROOT_DOMAIN} {
	handle {
		reverse_proxy localhost:{{.Routes.steward}}
	}
}
{{end}}

{{if .Routes.listmonk}}
# Listmonk Service
listmonk.{$CADDY_ROOT_DOMAIN} {
	handle {
		reverse_proxy localhost:{{.Routes.listmonk}}
	}
}
{{end}}
`
	t, err := template.New("caddyfile").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
