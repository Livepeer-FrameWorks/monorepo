package provisioner

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

const (
	defaultVictoriaMetricsImage = "victoriametrics/victoria-metrics:v1.122.0"
	defaultVMAAuthImage         = "victoriametrics/vmauth:v1.122.0"
	defaultVMAgentImage         = "victoriametrics/vmagent:v1.122.0"
	defaultGrafanaImage         = "grafana/grafana:12.2.0"
)

type VictoriaMetricsProvisioner struct {
	*BaseProvisioner
}

func NewVictoriaMetricsProvisioner(pool *ssh.Pool) *VictoriaMetricsProvisioner {
	return &VictoriaMetricsProvisioner{BaseProvisioner: NewBaseProvisioner("victoriametrics", pool)}
}

func (p *VictoriaMetricsProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "victoriametrics")
}

func (p *VictoriaMetricsProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "" {
		config.Mode = "docker"
	}
	if config.Mode != "docker" {
		return fmt.Errorf("victoriametrics only supports docker mode")
	}

	image, err := resolveObservabilityImage(config.Version, config.Image, "victoriametrics", defaultVictoriaMetricsImage, config.Metadata)
	if err != nil {
		return err
	}

	if err := p.RunRemoteCommand(ctx, host, "mkdir -p /opt/frameworks/victoriametrics /etc/frameworks /var/lib/frameworks/victoriametrics"); err != nil {
		return err
	}
	if err := writeProvisionerEnvFile(ctx, p.BaseProvisioner, host, "/etc/frameworks/victoriametrics.env", config.EnvVars); err != nil {
		return err
	}

	if password := strings.TrimSpace(config.EnvVars["VM_HTTP_AUTH_PASSWORD"]); password != "" {
		if err := uploadContent(ctx, p.BaseProvisioner, host, "/etc/frameworks/victoriametrics.password", password+"\n", 0o600); err != nil {
			return err
		}
	}

	compose := buildCustomCompose("victoriametrics", image, buildVictoriaMetricsComposeOptions(config))

	if err := deployCustomCompose(ctx, p.BaseProvisioner, host, "victoriametrics", compose, config.DeferStart); err != nil {
		return err
	}
	if config.DeferStart {
		fmt.Println("⏸ victoriametrics deployed but NOT started (missing required config)")
		return nil
	}
	fmt.Println("✓ victoriametrics provisioned in Docker mode")
	return nil
}

func (p *VictoriaMetricsProvisioner) Validate(ctx context.Context, host inventory.Host, _ ServiceConfig) error {
	return validateRunningContainer(ctx, p.BaseProvisioner, host, "victoriametrics")
}

func (p *VictoriaMetricsProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

type VMAgentProvisioner struct {
	*BaseProvisioner
}

func NewVMAgentProvisioner(pool *ssh.Pool) *VMAgentProvisioner {
	return &VMAgentProvisioner{BaseProvisioner: NewBaseProvisioner("vmagent", pool)}
}

func (p *VMAgentProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "vmagent")
}

func (p *VMAgentProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "" {
		config.Mode = "docker"
	}
	if config.Mode != "docker" {
		return fmt.Errorf("vmagent only supports docker mode")
	}

	image, err := resolveObservabilityImage(config.Version, config.Image, "vmagent", defaultVMAgentImage, config.Metadata)
	if err != nil {
		return err
	}

	err = p.RunRemoteCommand(ctx, host, "mkdir -p /opt/frameworks/vmagent /etc/frameworks")
	if err != nil {
		return err
	}
	err = writeProvisionerEnvFile(ctx, p.BaseProvisioner, host, "/etc/frameworks/vmagent.env", config.EnvVars)
	if err != nil {
		return err
	}

	scrapeConfig, err := buildVMAgentScrapeConfig(config.Metadata["scrape_targets"], config.EnvVars["VMAGENT_SCRAPE_INTERVAL"])
	if err != nil {
		return err
	}
	if err := uploadContent(ctx, p.BaseProvisioner, host, "/etc/frameworks/vmagent.yml", scrapeConfig, 0o644); err != nil {
		return err
	}

	if password := strings.TrimSpace(config.EnvVars["VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD"]); password != "" {
		if err := uploadContent(ctx, p.BaseProvisioner, host, "/etc/frameworks/vmagent.password", password+"\n", 0o600); err != nil {
			return err
		}
	}
	if strings.TrimSpace(config.EnvVars["VMAGENT_REMOTE_WRITE_URL"]) == "" {
		return fmt.Errorf("vmagent requires VMAGENT_REMOTE_WRITE_URL")
	}

	compose := buildCustomCompose("vmagent", image, buildVMAgentComposeOptions(config))

	if err := deployCustomCompose(ctx, p.BaseProvisioner, host, "vmagent", compose, config.DeferStart); err != nil {
		return err
	}
	if config.DeferStart {
		fmt.Println("⏸ vmagent deployed but NOT started (missing required config)")
		return nil
	}
	fmt.Println("✓ vmagent provisioned in Docker mode")
	return nil
}

func (p *VMAgentProvisioner) Validate(ctx context.Context, host inventory.Host, _ ServiceConfig) error {
	return validateRunningContainer(ctx, p.BaseProvisioner, host, "vmagent")
}

func (p *VMAgentProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

type VMAAuthProvisioner struct {
	*BaseProvisioner
}

func NewVMAAuthProvisioner(pool *ssh.Pool) *VMAAuthProvisioner {
	return &VMAAuthProvisioner{BaseProvisioner: NewBaseProvisioner("vmauth", pool)}
}

func (p *VMAAuthProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "vmauth")
}

func (p *VMAAuthProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "" {
		config.Mode = "docker"
	}
	if config.Mode != "docker" {
		return fmt.Errorf("vmauth only supports docker mode")
	}

	image, err := resolveObservabilityImage(config.Version, config.Image, "vmauth", defaultVMAAuthImage, config.Metadata)
	if err != nil {
		return err
	}

	err = p.RunRemoteCommand(ctx, host, "mkdir -p /opt/frameworks/vmauth /etc/frameworks")
	if err != nil {
		return err
	}
	err = writeProvisionerEnvFile(ctx, p.BaseProvisioner, host, "/etc/frameworks/vmauth.env", config.EnvVars)
	if err != nil {
		return err
	}

	authConfig, err := buildVMAAuthConfig(
		config.EnvVars["VMAUTH_UPSTREAM_WRITE_URL"],
		config.EnvVars["EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64"],
		config.EnvVars["VM_HTTP_AUTH_USERNAME"],
		config.EnvVars["VM_HTTP_AUTH_PASSWORD"],
	)
	if err != nil {
		return err
	}
	if err := uploadContent(ctx, p.BaseProvisioner, host, "/etc/frameworks/vmauth.yml", authConfig, 0o600); err != nil {
		return err
	}

	compose := buildCustomCompose("vmauth", image, buildVMAuthComposeOptions(config))

	if err := deployCustomCompose(ctx, p.BaseProvisioner, host, "vmauth", compose, config.DeferStart); err != nil {
		return err
	}
	if config.DeferStart {
		fmt.Println("⏸ vmauth deployed but NOT started (missing required config)")
		return nil
	}
	fmt.Println("✓ vmauth provisioned in Docker mode")
	return nil
}

func (p *VMAAuthProvisioner) Validate(ctx context.Context, host inventory.Host, _ ServiceConfig) error {
	return validateRunningContainer(ctx, p.BaseProvisioner, host, "vmauth")
}

func (p *VMAAuthProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

type GrafanaProvisioner struct {
	*BaseProvisioner
}

func NewGrafanaProvisioner(pool *ssh.Pool) *GrafanaProvisioner {
	return &GrafanaProvisioner{BaseProvisioner: NewBaseProvisioner("grafana", pool)}
}

func (p *GrafanaProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "grafana")
}

func (p *GrafanaProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "" {
		config.Mode = "docker"
	}
	if config.Mode != "docker" {
		return fmt.Errorf("grafana only supports docker mode")
	}

	image, err := resolveObservabilityImage(config.Version, config.Image, "grafana", defaultGrafanaImage, config.Metadata)
	if err != nil {
		return err
	}

	if err := p.RunRemoteCommand(ctx, host, "mkdir -p /opt/frameworks/grafana/provisioning/datasources /etc/frameworks /var/lib/frameworks/grafana"); err != nil {
		return err
	}
	if err := writeProvisionerEnvFile(ctx, p.BaseProvisioner, host, "/etc/frameworks/grafana.env", config.EnvVars); err != nil {
		return err
	}

	if datasourceURL := strings.TrimSpace(config.EnvVars["VICTORIAMETRICS_URL"]); datasourceURL != "" {
		ds := buildGrafanaDatasource(datasourceURL, config.EnvVars["VM_HTTP_AUTH_USERNAME"], config.EnvVars["VM_HTTP_AUTH_PASSWORD"])
		if err := uploadContent(ctx, p.BaseProvisioner, host, "/opt/frameworks/grafana/provisioning/datasources/frameworks.yaml", ds, 0o644); err != nil {
			return err
		}
	}

	compose := buildCustomCompose("grafana", image, buildGrafanaComposeOptions(config))

	if err := deployCustomCompose(ctx, p.BaseProvisioner, host, "grafana", compose, config.DeferStart); err != nil {
		return err
	}
	if config.DeferStart {
		fmt.Println("⏸ grafana deployed but NOT started (missing required config)")
		return nil
	}
	fmt.Println("✓ grafana provisioned in Docker mode")
	return nil
}

func (p *GrafanaProvisioner) Validate(ctx context.Context, host inventory.Host, _ ServiceConfig) error {
	return validateRunningContainer(ctx, p.BaseProvisioner, host, "grafana")
}

func (p *GrafanaProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

type customComposeOptions struct {
	Ports       []string
	Volumes     []string
	Network     string
	EnvFile     string
	Command     []string
	HostNetwork bool
}

// buildVictoriaMetricsComposeOptions returns the customComposeOptions used
// by both apply and drift to render /opt/frameworks/victoriametrics/docker-compose.yml.
func buildVictoriaMetricsComposeOptions(config ServiceConfig) customComposeOptions {
	retention := config.EnvVars["VM_RETENTION_PERIOD"]
	if retention == "" {
		retention = "30d"
	}
	passwordPath := ""
	if strings.TrimSpace(config.EnvVars["VM_HTTP_AUTH_PASSWORD"]) != "" {
		passwordPath = "/etc/frameworks/victoriametrics.password"
	}
	command := []string{
		"--storageDataPath=/storage",
		"--httpListenAddr=:8428",
		"--retentionPeriod=" + retention,
	}
	if username := strings.TrimSpace(config.EnvVars["VM_HTTP_AUTH_USERNAME"]); username != "" && passwordPath != "" {
		command = append(command,
			"--httpAuth.username="+username,
			"--httpAuth.password=file://"+passwordPath,
		)
	}
	volumes := []string{"/var/lib/frameworks/victoriametrics:/storage"}
	if passwordPath != "" {
		volumes = append(volumes, passwordPath+":"+passwordPath+":ro")
	}
	return customComposeOptions{
		Ports:   []string{fmt.Sprintf("%d:8428", resolvedServicePort(config, 8428))},
		Volumes: volumes,
		Network: "frameworks",
		Command: command,
	}
}

// buildVMAuthComposeOptions returns the compose options for vmauth.
func buildVMAuthComposeOptions(config ServiceConfig) customComposeOptions {
	return customComposeOptions{
		Ports: []string{fmt.Sprintf("%d:8427", resolvedServicePort(config, 8427))},
		Volumes: []string{
			"/etc/frameworks/vmauth.yml:/etc/frameworks/vmauth.yml:ro",
		},
		Network: "frameworks",
		Command: []string{
			"--httpListenAddr=:8427",
			"--auth.config=/etc/frameworks/vmauth.yml",
		},
	}
}

// buildGrafanaComposeOptions returns the compose options for grafana.
func buildGrafanaComposeOptions(config ServiceConfig) customComposeOptions {
	return customComposeOptions{
		Ports: []string{fmt.Sprintf("%d:3000", resolvedServicePort(config, 3000))},
		Volumes: []string{
			"/var/lib/frameworks/grafana:/var/lib/grafana",
			"/opt/frameworks/grafana/provisioning:/etc/grafana/provisioning",
		},
		Network: "frameworks",
		EnvFile: "/etc/frameworks/grafana.env",
	}
}

// buildVMAgentComposeOptions returns the compose options for vmagent.
func buildVMAgentComposeOptions(config ServiceConfig) customComposeOptions {
	passwordPath := ""
	if strings.TrimSpace(config.EnvVars["VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD"]) != "" {
		passwordPath = "/etc/frameworks/vmagent.password"
	}
	remoteWriteURL := strings.TrimSpace(config.EnvVars["VMAGENT_REMOTE_WRITE_URL"])
	command := []string{
		"--httpListenAddr=:8429",
		"--promscrape.config=/etc/frameworks/vmagent.yml",
		"--remoteWrite.url=" + remoteWriteURL,
	}
	if username := strings.TrimSpace(config.EnvVars["VMAGENT_REMOTE_WRITE_BASIC_AUTH_USERNAME"]); username != "" && passwordPath != "" {
		command = append(command,
			"--remoteWrite.basicAuth.username="+username,
			"--remoteWrite.basicAuth.password=file://"+passwordPath,
		)
	}
	volumes := []string{"/etc/frameworks/vmagent.yml:/etc/frameworks/vmagent.yml:ro"}
	if passwordPath != "" {
		volumes = append(volumes, passwordPath+":"+passwordPath+":ro")
	}
	return customComposeOptions{
		HostNetwork: true,
		Volumes:     volumes,
		Command:     command,
	}
}

func buildCustomCompose(serviceName, image string, opts customComposeOptions) string {
	var b strings.Builder
	b.WriteString("version: '3.8'\n\nservices:\n")
	b.WriteString(fmt.Sprintf("  %s:\n", serviceName))
	b.WriteString(fmt.Sprintf("    image: %s\n", image))
	b.WriteString(fmt.Sprintf("    container_name: frameworks-%s\n", serviceName))
	b.WriteString("    restart: always\n")
	if opts.HostNetwork {
		b.WriteString("    network_mode: host\n")
	}
	if len(opts.Ports) > 0 {
		b.WriteString("    ports:\n")
		for _, port := range opts.Ports {
			b.WriteString(fmt.Sprintf("      - %s\n", yamlQuote(port)))
		}
	}
	if opts.EnvFile != "" {
		b.WriteString("    env_file:\n")
		b.WriteString(fmt.Sprintf("      - %s\n", opts.EnvFile))
	}
	if len(opts.Command) > 0 {
		b.WriteString("    command:\n")
		for _, arg := range opts.Command {
			b.WriteString(fmt.Sprintf("      - %s\n", yamlQuote(arg)))
		}
	}
	if len(opts.Volumes) > 0 {
		b.WriteString("    volumes:\n")
		for _, volume := range opts.Volumes {
			b.WriteString(fmt.Sprintf("      - %s\n", yamlQuote(volume)))
		}
	}
	if opts.Network != "" && !opts.HostNetwork {
		b.WriteString("    networks:\n")
		b.WriteString(fmt.Sprintf("      - %s\n", opts.Network))
		b.WriteString("\nnetworks:\n")
		b.WriteString(fmt.Sprintf("  %s:\n", opts.Network))
		b.WriteString("    driver: bridge\n")
	}
	return b.String()
}

func deployCustomCompose(ctx context.Context, base *BaseProvisioner, host inventory.Host, serviceName, compose string, deferStart bool) error {
	remoteDir := filepath.Join("/opt/frameworks", serviceName)
	if err := base.RunRemoteCommand(ctx, host, "mkdir -p "+remoteDir); err != nil {
		return err
	}
	if err := uploadContent(ctx, base, host, filepath.Join(remoteDir, "docker-compose.yml"), compose, 0o644); err != nil {
		return err
	}

	commands := []string{
		fmt.Sprintf("cd %s && docker compose pull", remoteDir),
	}
	if !deferStart {
		commands = append(commands, fmt.Sprintf("cd %s && docker compose up -d", remoteDir))
	}
	for _, command := range commands {
		if err := base.RunRemoteCommand(ctx, host, command); err != nil {
			return err
		}
	}
	return nil
}

func writeProvisionerEnvFile(ctx context.Context, base *BaseProvisioner, host inventory.Host, remotePath string, envVars map[string]string) error {
	if len(envVars) == 0 {
		return nil
	}

	keys := make([]string, 0, len(envVars))
	for key := range envVars {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, envVars[key]))
	}

	return uploadContent(ctx, base, host, remotePath, strings.Join(lines, "\n")+"\n", 0o600)
}

func buildVMAgentScrapeConfig(raw interface{}, interval string) (string, error) {
	targets, ok := raw.([]map[string]interface{})
	if !ok || len(targets) == 0 {
		return "", fmt.Errorf("vmagent requires scrape_targets metadata")
	}
	if strings.TrimSpace(interval) == "" {
		interval = "30s"
	}

	var b strings.Builder
	b.WriteString("global:\n")
	b.WriteString(fmt.Sprintf("  scrape_interval: %s\n", yamlBare(interval)))
	b.WriteString("scrape_configs:\n")
	for _, target := range targets {
		jobName, ok := target["job_name"].(string)
		if !ok || jobName == "" {
			continue
		}
		targetList, ok := target["targets"].([]string)
		path, _ := target["path"].(string)                //nolint:errcheck // zero value acceptable
		labels, _ := target["labels"].(map[string]string) //nolint:errcheck // zero value acceptable
		if !ok || len(targetList) == 0 {
			continue
		}
		if path == "" {
			path = "/metrics"
		}
		b.WriteString(fmt.Sprintf("  - job_name: %s\n", yamlBare(jobName)))
		b.WriteString(fmt.Sprintf("    metrics_path: %s\n", yamlBare(path)))
		b.WriteString("    static_configs:\n")
		b.WriteString("      - targets:\n")
		for _, targetAddr := range targetList {
			b.WriteString(fmt.Sprintf("          - %s\n", yamlQuote(targetAddr)))
		}
		if len(labels) > 0 {
			labelKeys := make([]string, 0, len(labels))
			for key := range labels {
				labelKeys = append(labelKeys, key)
			}
			sort.Strings(labelKeys)
			b.WriteString("        labels:\n")
			for _, key := range labelKeys {
				b.WriteString(fmt.Sprintf("          %s: %s\n", key, yamlQuote(labels[key])))
			}
		}
	}
	return b.String(), nil
}

func buildGrafanaDatasource(url, username, password string) string {
	var b strings.Builder
	b.WriteString("apiVersion: 1\n")
	b.WriteString("datasources:\n")
	b.WriteString("  - name: VictoriaMetrics\n")
	b.WriteString("    type: prometheus\n")
	b.WriteString("    access: proxy\n")
	b.WriteString(fmt.Sprintf("    url: %s\n", yamlQuote(url)))
	b.WriteString("    isDefault: true\n")
	b.WriteString("    editable: false\n")
	b.WriteString("    jsonData:\n")
	b.WriteString("      httpMethod: POST\n")
	if username != "" && password != "" {
		b.WriteString("    basicAuth: true\n")
		b.WriteString(fmt.Sprintf("    basicAuthUser: %s\n", yamlQuote(username)))
		b.WriteString("    secureJsonData:\n")
		b.WriteString(fmt.Sprintf("      basicAuthPassword: %s\n", yamlQuote(password)))
	}
	return b.String()
}

func buildVMAAuthConfig(upstreamWriteURL, publicKeyB64, upstreamUsername, upstreamPassword string) (string, error) {
	upstreamWriteURL = strings.TrimSpace(upstreamWriteURL)
	if upstreamWriteURL == "" {
		return "", fmt.Errorf("vmauth requires VMAUTH_UPSTREAM_WRITE_URL")
	}
	publicKeyB64 = strings.TrimSpace(publicKeyB64)
	if publicKeyB64 == "" {
		return "", fmt.Errorf("vmauth requires EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64")
	}
	publicKeyPEM, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return "", fmt.Errorf("decode vmauth public key: %w", err)
	}
	proxyURL, err := vmauthProxyURL(upstreamWriteURL, upstreamUsername, upstreamPassword)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("users:\n")
	b.WriteString("  - jwt:\n")
	b.WriteString("      public_keys:\n")
	b.WriteString("        - |\n")
	for _, line := range strings.Split(strings.TrimSpace(string(publicKeyPEM)), "\n") {
		b.WriteString("          " + line + "\n")
	}
	b.WriteString("    url_prefix: " + yamlQuote(proxyURL) + "\n")
	return b.String(), nil
}

// vmauthProxyURL builds the upstream URL with credentials embedded in userinfo.
// VMAuth's url_prefix requires credentials in the URL for basic-auth forwarding;
// there is no separate auth directive. The resulting vmauth.yml must be 0600.
func vmauthProxyURL(upstreamWriteURL, upstreamUsername, upstreamPassword string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(upstreamWriteURL))
	if err != nil {
		return "", fmt.Errorf("parse VMAUTH_UPSTREAM_WRITE_URL: %w", err)
	}
	if username := strings.TrimSpace(upstreamUsername); username != "" {
		parsed.User = url.UserPassword(username, upstreamPassword)
	}
	rendered := parsed.String()
	separator := "?"
	if strings.Contains(rendered, "?") {
		separator = "&"
	}
	return rendered + separator + "extra_label={{.MetricsExtraLabels}}", nil
}

func resolveObservabilityImage(version, explicitImage, serviceName, fallback string, metadata map[string]interface{}) (string, error) {
	if strings.TrimSpace(explicitImage) != "" {
		return explicitImage, nil
	}

	info, err := resolveObservabilityServiceInfo(version, serviceName, metadata)
	if err == nil && info != nil && strings.TrimSpace(info.FullImage) != "" {
		return info.FullImage, nil
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("no image available for %s", serviceName)
}

// resolveVMAgentArtifact returns the vmagent artifact (url + checksum) from
// the release manifest pinned to channel, for the requested os/arch pair.
func resolveVMAgentArtifact(channel, osName, arch string, metadata map[string]any) (*gitops.Artifact, error) {
	return resolveInfraArtifactFromChannel("vmagent", osName+"-"+arch, channel, metadata)
}

func resolveObservabilityServiceInfo(version, serviceName string, metadata map[string]interface{}) (*gitops.ServiceInfo, error) {
	channel, resolvedVersion := gitops.ResolveVersion(version)
	manifest, err := fetchGitopsManifest(channel, resolvedVersion, metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch gitops manifest: %w", err)
	}

	info, err := manifest.GetServiceInfo(serviceName)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func validateRunningContainer(ctx context.Context, base *BaseProvisioner, host inventory.Host, serviceName string) error {
	state, err := base.CheckExists(ctx, host, serviceName)
	if err != nil {
		return err
	}
	if state == nil || !state.Exists || !state.Running {
		return fmt.Errorf("%s is not running", serviceName)
	}
	return nil
}

func uploadContent(ctx context.Context, base *BaseProvisioner, host inventory.Host, remotePath, content string, mode os.FileMode) error {
	tmpFile, err := os.CreateTemp("", "frameworks-provisioner-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(content); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := base.RunRemoteCommand(ctx, host, "mkdir -p "+filepath.Dir(remotePath)); err != nil {
		return err
	}
	return base.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpFile.Name(),
		RemotePath: remotePath,
		Mode:       uint32(mode),
	})
}

func (b *BaseProvisioner) RunRemoteCommand(ctx context.Context, host inventory.Host, command string) error {
	result, err := b.RunCommand(ctx, host, command)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("command failed: %s", strings.TrimSpace(result.Stderr))
	}
	return nil
}

func resolvedServicePort(config ServiceConfig, fallback int) int {
	if config.Port != 0 {
		return config.Port
	}
	return fallback
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}

func yamlBare(value string) string {
	return strings.Trim(strconv.Quote(value), `"`)
}
