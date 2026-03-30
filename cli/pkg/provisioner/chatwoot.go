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

const defaultChatwootImage = "chatwoot/chatwoot:v3.14.0"

// ChatwootProvisioner provisions the self-hosted Chatwoot support chat (app + Sidekiq worker).
type ChatwootProvisioner struct {
	*BaseProvisioner
}

// NewChatwootProvisioner creates a new Chatwoot provisioner.
func NewChatwootProvisioner(pool *ssh.Pool) *ChatwootProvisioner {
	return &ChatwootProvisioner{
		BaseProvisioner: NewBaseProvisioner("chatwoot", pool),
	}
}

// Detect checks if Chatwoot containers exist.
func (c *ChatwootProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return c.CheckExists(ctx, host, "chatwoot")
}

// Provision deploys Chatwoot (app + worker) via Docker Compose.
func (c *ChatwootProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	fmt.Println("Provisioning chatwoot (app + worker) in Docker mode...")

	state, err := c.Detect(ctx, host)
	if err != nil {
		state = nil
	}

	image := config.Image
	if image == "" {
		image = defaultChatwootImage
	}
	if skip, reason := shouldSkipProvision(state, config, "", image); skip {
		fmt.Printf("Service %s already running (%s), skipping...\n", c.name, reason)
		return nil
	}

	// Generate SECRET_KEY_BASE if not already set on the remote host
	secretKey, err := c.ensureSecretKey(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to generate SECRET_KEY_BASE: %w", err)
	}

	// Build Chatwoot-specific env file
	envFile := "/etc/frameworks/chatwoot.env"
	if err = c.writeChatwootEnv(ctx, host, envFile, config, secretKey); err != nil {
		return fmt.Errorf("failed to write chatwoot env file: %w", err)
	}

	// Generate and upload docker-compose
	port := config.Port
	if port == 0 {
		port = 18092
	}
	composeYAML := c.generateCompose(image, envFile, port)

	tmpDir, err := os.MkdirTemp("", "chatwoot-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err = os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	remotePath := "/opt/frameworks/chatwoot/docker-compose.yml"
	if err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remotePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	// Pull images
	pullCmd := "cd /opt/frameworks/chatwoot && docker compose pull"
	result, err := c.RunCommand(ctx, host, pullCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to pull chatwoot images: %w\nStderr: %s", err, result.Stderr)
	}

	if config.DeferStart {
		fmt.Println("⏸ chatwoot deployed but NOT started (missing required config)")
		return nil
	}

	// Start containers
	upCmd := "cd /opt/frameworks/chatwoot && docker compose up -d"
	result, err = c.RunCommand(ctx, host, upCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to start chatwoot: %w\nStderr: %s", err, result.Stderr)
	}

	fmt.Println("✓ chatwoot provisioned in Docker mode")
	return nil
}

// ensureSecretKey reads or generates a Rails SECRET_KEY_BASE on the remote host.
func (c *ChatwootProvisioner) ensureSecretKey(ctx context.Context, host inventory.Host) (string, error) {
	keyFile := "/etc/frameworks/.chatwoot_secret_key"

	// Try reading existing key
	result, err := c.RunCommand(ctx, host, fmt.Sprintf("cat %s 2>/dev/null", keyFile))
	if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return strings.TrimSpace(result.Stdout), nil
	}

	// Generate new key
	result, err = c.RunCommand(ctx, host, fmt.Sprintf("openssl rand -hex 64 | tee %s", keyFile))
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("openssl rand failed: %w", err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

// writeChatwootEnv writes the Chatwoot env file with PG, Redis, and SMTP config.
func (c *ChatwootProvisioner) writeChatwootEnv(ctx context.Context, host inventory.Host, envFilePath string, config ServiceConfig, secretKey string) error {
	env := make(map[string]string)

	// Rails core
	env["SECRET_KEY_BASE"] = secretKey
	env["RAILS_ENV"] = "production"
	env["RAILS_LOG_TO_STDOUT"] = "true"
	env["LOG_LEVEL"] = "info"

	// PostgreSQL — map from CLI-generated vars
	env["POSTGRES_HOST"] = config.EnvVars["DATABASE_HOST"]
	env["POSTGRES_PORT"] = config.EnvVars["DATABASE_PORT"]
	env["POSTGRES_DATABASE"] = "chatwoot"
	dbUser := config.EnvVars["DATABASE_USER"]
	if dbUser == "" {
		dbUser = "postgres"
	}
	env["POSTGRES_USERNAME"] = dbUser
	env["POSTGRES_PASSWORD"] = config.EnvVars["DATABASE_PASSWORD"]

	// Redis — map from CLI-generated REDIS_CHATWOOT_ADDR
	if redisAddr := config.EnvVars["REDIS_CHATWOOT_ADDR"]; redisAddr != "" {
		env["REDIS_URL"] = fmt.Sprintf("redis://%s", redisAddr)
	}

	// SMTP passthrough from secrets env_file
	for _, key := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD"} {
		if v := config.EnvVars[key]; v != "" {
			chatwootKey := key
			if key == "SMTP_HOST" {
				chatwootKey = "SMTP_ADDRESS"
			}
			if key == "SMTP_USER" {
				chatwootKey = "SMTP_USERNAME"
			}
			if key == "SMTP_PASSWORD" {
				chatwootKey = "SMTP_PASSWORD"
			}
			env[chatwootKey] = v
		}
	}
	env["SMTP_AUTHENTICATION"] = "login"
	env["SMTP_ENABLE_STARTTLS_AUTO"] = "true"

	// FRONTEND_URL from config or env
	if v := config.EnvVars["CHATWOOT_FRONTEND_URL"]; v != "" {
		env["FRONTEND_URL"] = v
	}
	if v := config.EnvVars["CHATWOOT_MAILER_EMAIL"]; v != "" {
		env["MAILER_SENDER_EMAIL"] = v
	}

	// Required keys are always written (even if empty) so Chatwoot has a complete config.
	// Optional keys (SMTP, frontend URL, etc.) only appear when configured.
	requiredKeys := []string{
		"SECRET_KEY_BASE", "RAILS_ENV", "RAILS_LOG_TO_STDOUT", "LOG_LEVEL",
		"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DATABASE",
		"POSTGRES_USERNAME", "POSTGRES_PASSWORD",
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
	result, err := c.RunCommand(ctx, host, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write env file: %s", result.Stderr)
	}
	return nil
}

// generateCompose produces a docker-compose.yml for Chatwoot app + Sidekiq worker.
func (c *ChatwootProvisioner) generateCompose(image, envFile string, port int) string {
	return fmt.Sprintf(`version: '3.8'

services:
  chatwoot:
    image: %s
    container_name: frameworks-chatwoot
    restart: always
    entrypoint: ["/bin/sh", "-c"]
    command:
      - |
        bundle exec rails db:chatwoot_prepare
        bundle exec rails s -p 3000 -b '0.0.0.0'
    env_file:
      - %s
    ports:
      - "%d:3000"
    networks:
      - frameworks
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://127.0.0.1:3000/api"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 60s
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "3"

  chatwoot-worker:
    image: %s
    container_name: frameworks-chatwoot-worker
    restart: always
    command: bundle exec sidekiq -C config/sidekiq.yml
    env_file:
      - %s
    networks:
      - frameworks
    depends_on:
      chatwoot:
        condition: service_healthy
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "3"

networks:
  frameworks:
    driver: bridge
`, image, envFile, port, image, envFile)
}

// Validate checks if Chatwoot is healthy.
func (c *ChatwootProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.HTTPChecker{
		Path:    "/api",
		Timeout: 10,
	}
	port := config.Port
	if port == 0 {
		port = 18092
	}
	result := checker.Check(host.ExternalIP, port)
	if !result.OK {
		return fmt.Errorf("chatwoot health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op — Rails migrations run on container startup.
func (c *ChatwootProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

// Cleanup stops Chatwoot containers.
func (c *ChatwootProvisioner) Cleanup(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	cmd := "cd /opt/frameworks/chatwoot && docker compose down 2>/dev/null || true"
	_, _ = c.RunCommand(ctx, host, cmd)
	return nil
}
