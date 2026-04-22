package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

const defaultListmonkImage = "listmonk/listmonk:v4.0.1"

// ListmonkProvisioner provisions the self-hosted Listmonk newsletter service.
// Listmonk uses non-standard env vars (LISTMONK_db__host) and a custom entrypoint,
// so it needs its own provisioner instead of FlexibleProvisioner.
type ListmonkProvisioner struct {
	*BaseProvisioner
}

// NewListmonkProvisioner creates a new Listmonk provisioner.
func NewListmonkProvisioner(pool *ssh.Pool) *ListmonkProvisioner {
	return &ListmonkProvisioner{
		BaseProvisioner: NewBaseProvisioner("listmonk", pool),
	}
}

// Detect checks if Listmonk container exists.
func (l *ListmonkProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return l.CheckExists(ctx, host, "listmonk")
}

// Provision deploys Listmonk via Docker Compose.
func (l *ListmonkProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	fmt.Println("Provisioning listmonk in Docker mode...")

	image := config.Image
	if image == "" {
		image = defaultListmonkImage
	}

	// Write Listmonk-specific env file
	envFile := "/etc/frameworks/listmonk.env"
	if err := l.writeListmonkEnv(ctx, host, envFile, config); err != nil {
		return fmt.Errorf("failed to write listmonk env file: %w", err)
	}

	// Generate and upload docker-compose
	port := config.Port
	if port == 0 {
		port = 9001
	}
	composeYAML := l.generateCompose(image, envFile, port)

	tmpDir, err := os.MkdirTemp("", "listmonk-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err = os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	remotePath := "/opt/frameworks/listmonk/docker-compose.yml"
	if err = l.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remotePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	// Pull image
	pullCmd := "cd /opt/frameworks/listmonk && docker compose pull"
	result, err := l.RunCommand(ctx, host, pullCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to pull listmonk image: %w\nStderr: %s", err, result.Stderr)
	}

	if config.DeferStart {
		fmt.Println("⏸ listmonk deployed but NOT started (missing required config)")
		return nil
	}

	// Start container
	upCmd := "cd /opt/frameworks/listmonk && docker compose up -d"
	result, err = l.RunCommand(ctx, host, upCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to start listmonk: %w\nStderr: %s", err, result.Stderr)
	}

	fmt.Println("✓ listmonk provisioned in Docker mode")
	return nil
}

// writeListmonkEnv writes the Listmonk env file with LISTMONK_db__* format.
func (l *ListmonkProvisioner) writeListmonkEnv(ctx context.Context, host inventory.Host, envFilePath string, config ServiceConfig) error {
	env := make(map[string]string)

	// Listmonk uses its own env var format
	env["LISTMONK_app__address"] = "0.0.0.0:9000"
	env["LISTMONK_db__host"] = config.EnvVars["DATABASE_HOST"]
	env["LISTMONK_db__port"] = config.EnvVars["DATABASE_PORT"]
	dbUser := config.EnvVars["DATABASE_USER"]
	if dbUser == "" {
		dbUser = "postgres"
	}
	env["LISTMONK_db__user"] = dbUser
	env["LISTMONK_db__password"] = config.EnvVars["DATABASE_PASSWORD"]
	env["LISTMONK_db__database"] = "listmonk"
	env["LISTMONK_db__ssl_mode"] = "disable"

	// SMTP passthrough
	if v := config.EnvVars["SMTP_HOST"]; v != "" {
		env["LISTMONK_app__smtp__host"] = v
	}
	if v := config.EnvVars["SMTP_PORT"]; v != "" {
		env["LISTMONK_app__smtp__port"] = v
	}
	if v := config.EnvVars["SMTP_USER"]; v != "" {
		env["LISTMONK_app__smtp__username"] = v
	}
	if v := config.EnvVars["SMTP_PASSWORD"]; v != "" {
		env["LISTMONK_app__smtp__password"] = v
	}
	env["LISTMONK_app__smtp__auth_protocol"] = "login"
	env["LISTMONK_app__smtp__tls_type"] = "STARTTLS"
	if v := config.EnvVars["FROM_EMAIL"]; v != "" {
		env["LISTMONK_app__from_email"] = v
	}

	// Required keys are always written (even if empty) so Listmonk has a complete config.
	// Optional keys (SMTP) only appear when configured.
	requiredKeys := []string{
		"LISTMONK_app__address",
		"LISTMONK_db__host", "LISTMONK_db__port", "LISTMONK_db__user",
		"LISTMONK_db__password", "LISTMONK_db__database", "LISTMONK_db__ssl_mode",
	}
	written := make(map[string]bool, len(requiredKeys))
	var lines []string
	for _, k := range requiredKeys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, env[k]))
		written[k] = true
	}
	var optKeys []string
	for k, v := range env {
		if !written[k] && v != "" {
			optKeys = append(optKeys, k)
		}
	}
	sort.Strings(optKeys)
	for _, k := range optKeys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, env[k]))
	}
	content := strings.Join(lines, "\n") + "\n"

	writeCmd := fmt.Sprintf("mkdir -p /etc/frameworks && cat > %s << 'ENVEOF'\n%sENVEOF", envFilePath, content)
	result, err := l.RunCommand(ctx, host, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write env file: %s", result.Stderr)
	}
	return nil
}

// generateCompose produces a docker-compose.yml for Listmonk.
func (l *ListmonkProvisioner) generateCompose(image, envFile string, port int) string {
	return fmt.Sprintf(`version: '3.8'

services:
  listmonk:
    image: %s
    container_name: frameworks-listmonk
    restart: always
    entrypoint: ["sh", "-c", "./listmonk --install --idempotent --yes && ./listmonk"]
    env_file:
      - %s
    ports:
      - "%d:9000"
    networks:
      - frameworks
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://127.0.0.1:9000/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 30s
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "3"

networks:
  frameworks:
    driver: bridge
`, image, envFile, port)
}

// Validate checks if Listmonk is healthy.
func (l *ListmonkProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.HTTPChecker{
		Path:    "/health",
		Timeout: 5,
	}
	port := config.Port
	if port == 0 {
		port = 9001
	}
	result := checker.Check(host.ExternalIP, port)
	if !result.OK {
		return fmt.Errorf("listmonk health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op — Listmonk runs --install --idempotent on startup.
func (l *ListmonkProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

// Cleanup stops Listmonk container.
func (l *ListmonkProvisioner) Cleanup(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	cmd := "cd /opt/frameworks/listmonk && docker compose down 2>/dev/null || true"
	_, _ = l.RunCommand(ctx, host, cmd)
	return nil
}
