package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// RedisProvisioner provisions named Redis instances in Docker or native mode.
type RedisProvisioner struct {
	*BaseProvisioner
	executor *ansible.Executor
}

// NewRedisProvisioner creates a new Redis provisioner.
func NewRedisProvisioner(pool *ssh.Pool) (*RedisProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("failed to create ansible executor: %w", err)
	}

	return &RedisProvisioner{
		BaseProvisioner: NewBaseProvisioner("redis", pool),
		executor:        executor,
	}, nil
}

// Detect checks if a Redis instance is running.
func (r *RedisProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return r.CheckExists(ctx, host, "redis")
}

// Provision installs and starts a Redis instance.
func (r *RedisProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	instanceName, _ := config.Metadata["instance_name"].(string)
	if instanceName == "" {
		instanceName = "default"
	}

	state, err := r.Detect(ctx, host)
	if err == nil && state.Exists && state.Running && !config.Force {
		fmt.Printf("Redis instance %s already running, skipping...\n", instanceName)
		return nil
	}

	switch config.Mode {
	case "docker":
		return r.provisionDocker(ctx, host, config, instanceName)
	case "native":
		return r.provisionNative(ctx, host, config, instanceName)
	default:
		return fmt.Errorf("unsupported mode: %s (must be docker or native)", config.Mode)
	}
}

// provisionDocker provisions Redis as a Docker container.
func (r *RedisProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, instanceName string) error {
	fmt.Printf("Provisioning Redis instance %q in Docker mode...\n", instanceName)

	port := config.Port
	if port == 0 {
		port = 6379
	}

	version := config.Version
	if version == "" {
		version = "7"
	}

	image := config.Image
	if image == "" {
		image = fmt.Sprintf("redis:%s-alpine", version)
	}

	password, _ := config.Metadata["password"].(string)

	// Build redis-server command args
	cmdArgs := buildRedisCommandArgs(config.Metadata, password)

	serviceName := fmt.Sprintf("redis-%s", instanceName)

	// Generate env file (for password reference if needed)
	envVars := map[string]string{}
	if password != "" {
		envVars["REDIS_PASSWORD"] = password
	}

	envFileContent := GenerateEnvFile(serviceName, envVars)
	tmpEnvFile := filepath.Join(os.TempDir(), serviceName+".env")
	if writeErr := os.WriteFile(tmpEnvFile, []byte(envFileContent), 0600); writeErr != nil {
		return writeErr
	}
	defer os.Remove(tmpEnvFile)

	remoteEnvFile := fmt.Sprintf("/etc/frameworks/%s.env", serviceName)
	if uploadErr := r.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpEnvFile,
		RemotePath: remoteEnvFile,
		Mode:       0600,
	}); uploadErr != nil {
		return uploadErr
	}

	// Build health check command
	healthTest := []string{"CMD", "redis-cli"}
	if password != "" {
		healthTest = append(healthTest, "-a", password)
	}
	healthTest = append(healthTest, "ping")

	composeData := DockerComposeData{
		ServiceName: serviceName,
		Image:       image,
		Port:        port,
		EnvFile:     remoteEnvFile,
		HealthCheck: &HealthCheckConfig{
			Test:     healthTest,
			Interval: "10s",
			Timeout:  "5s",
			Retries:  5,
		},
		Networks: []string{"frameworks"},
		Volumes: []string{
			fmt.Sprintf("/var/lib/frameworks/%s:/data", serviceName),
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	// Append command args to the generated compose (template doesn't support command)
	if cmdArgs != "" {
		composeYAML = appendComposeCommand(composeYAML, serviceName, cmdArgs)
	}

	tmpDir, err := os.MkdirTemp("", serviceName+"-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	remotePath := fmt.Sprintf("/opt/frameworks/%s", serviceName)
	if _, err := r.RunCommand(ctx, host, fmt.Sprintf("mkdir -p %s", remotePath)); err != nil {
		return fmt.Errorf("failed to create remote directory: %w", err)
	}

	remoteComposePath := fmt.Sprintf("%s/docker-compose.yml", remotePath)
	if err := r.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remoteComposePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	commands := []string{
		fmt.Sprintf("cd %s", remotePath),
		"docker compose pull",
		"docker compose up -d",
	}

	for _, cmd := range commands {
		result, err := r.RunCommand(ctx, host, cmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose command failed: %s\nStderr: %s", cmd, result.Stderr)
		}
	}

	fmt.Printf("✓ Redis instance %q provisioned in Docker mode\n", instanceName)
	return nil
}

// provisionNative provisions Redis using Ansible.
func (r *RedisProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, instanceName string) error {
	fmt.Printf("Provisioning Redis instance %q in native mode...\n", instanceName)

	password, _ := config.Metadata["password"].(string)
	port := config.Port
	if port == 0 {
		port = 6379
	}

	hostID := host.Address
	if hostID == "" {
		hostID = "localhost"
	}

	playbook := GenerateRedisPlaybook(hostID, instanceName, port, password, config.Metadata)

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    hostID,
		Address: host.Address,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": host.SSHKey,
		},
	})

	opts := ansible.ExecuteOptions{
		Verbose: true,
	}

	result, err := r.executor.ExecutePlaybook(ctx, playbook, inv, opts)
	if err != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", err, result.Output)
	}

	if !result.Success {
		return fmt.Errorf("ansible playbook failed with %d failures\nOutput: %s",
			result.PlaybookRun.Failures, result.Output)
	}

	fmt.Printf("✓ Redis instance %q provisioned in native mode\n", instanceName)
	return nil
}

// Validate checks if Redis is healthy via TCP.
func (r *RedisProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.TCPChecker{}
	result := checker.Check(host.Address, config.Port)
	if !result.OK {
		return fmt.Errorf("redis health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for Redis.
func (r *RedisProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

// buildRedisCommandArgs constructs redis-server CLI flags from metadata.
func buildRedisCommandArgs(metadata map[string]interface{}, password string) string {
	args := []string{"redis-server", "--appendonly", "yes"}

	if password != "" {
		args = append(args, "--requirepass", password)
	}

	// Extract redis_* config keys from metadata
	for key, val := range metadata {
		if !strings.HasPrefix(key, "redis_") {
			continue
		}
		directive := strings.TrimPrefix(key, "redis_")
		if strVal, ok := val.(string); ok {
			args = append(args, fmt.Sprintf("--%s", directive), strVal)
		}
	}

	return strings.Join(args, " ")
}

// appendComposeCommand injects a command directive into generated compose YAML.
func appendComposeCommand(composeYAML, serviceName, command string) string {
	// Insert command after the image line
	target := fmt.Sprintf("    container_name: frameworks-%s\n    restart: always", serviceName)
	replacement := fmt.Sprintf("    container_name: frameworks-%s\n    restart: always\n    command: %s", serviceName, command)
	return strings.Replace(composeYAML, target, replacement, 1)
}

// GenerateRedisPlaybook creates an Ansible playbook for native Redis installation.
func GenerateRedisPlaybook(host, instanceName string, port int, password string, metadata map[string]interface{}) *ansible.Playbook {
	playbook := ansible.NewPlaybook("Provision Redis", host)

	// Build redis.conf directives
	configLines := []string{
		fmt.Sprintf("port %d", port),
		"bind 0.0.0.0",
		"appendonly yes",
		"daemonize no",
		fmt.Sprintf("dir /var/lib/redis-%s", instanceName),
	}

	if password != "" {
		configLines = append(configLines, fmt.Sprintf("requirepass %s", password))
	}

	for key, val := range metadata {
		if !strings.HasPrefix(key, "redis_") {
			continue
		}
		directive := strings.TrimPrefix(key, "redis_")
		if strVal, ok := val.(string); ok {
			configLines = append(configLines, fmt.Sprintf("%s %s", directive, strVal))
		}
	}

	configContent := strings.Join(configLines, "\n") + "\n"
	serviceName := fmt.Sprintf("frameworks-redis-%s", instanceName)
	confPath := fmt.Sprintf("/etc/redis/redis-%s.conf", instanceName)
	dataDir := fmt.Sprintf("/var/lib/redis-%s", instanceName)

	play := ansible.Play{
		Name:        fmt.Sprintf("Install and configure Redis instance %s", instanceName),
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []ansible.Task{
			{
				Name:   "Install Redis server",
				Module: "apt",
				Args: map[string]interface{}{
					"name":             "redis-server",
					"state":            "present",
					"update_cache":     true,
					"cache_valid_time": 3600,
				},
			},
			{
				Name:   "Create Redis data directory",
				Module: "file",
				Args: map[string]interface{}{
					"path":  dataDir,
					"state": "directory",
					"owner": "redis",
					"group": "redis",
					"mode":  "0750",
				},
			},
			{
				Name:   "Write Redis configuration",
				Module: "copy",
				Args: map[string]interface{}{
					"content": configContent,
					"dest":    confPath,
					"owner":   "redis",
					"group":   "redis",
					"mode":    "0640",
				},
				Notify: []string{fmt.Sprintf("restart %s", serviceName)},
			},
			{
				Name:   "Create systemd unit for Redis",
				Module: "copy",
				Args: map[string]interface{}{
					"content": generateRedisSystemdUnit(instanceName, confPath),
					"dest":    fmt.Sprintf("/etc/systemd/system/%s.service", serviceName),
					"mode":    "0644",
				},
				Notify: []string{"reload systemd", fmt.Sprintf("restart %s", serviceName)},
			},
			{
				Name:   "Enable Redis service",
				Module: "systemd",
				Args: map[string]interface{}{
					"name":    serviceName,
					"enabled": true,
					"state":   "started",
				},
			},
		},
		Handlers: []ansible.Handler{
			{
				Name:   "reload systemd",
				Module: "systemd",
				Args: map[string]interface{}{
					"daemon_reload": true,
				},
			},
			{
				Name:   fmt.Sprintf("restart %s", serviceName),
				Module: "systemd",
				Args: map[string]interface{}{
					"name":  serviceName,
					"state": "restarted",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// generateRedisSystemdUnit creates a systemd unit file for a Redis instance.
func generateRedisSystemdUnit(instanceName, confPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Frameworks Redis (%s)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=redis
Group=redis
ExecStart=/usr/bin/redis-server %s
Restart=always
RestartSec=5s
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`, instanceName, confPath)
}
