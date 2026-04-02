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

const (
	defaultRedisEngine   = "valkey"
	defaultValkeyVersion = "8.1"
	defaultRedisVersion  = "7.2.4"
)

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
	engine, err := resolveRedisEngine(config.Metadata)
	if err != nil {
		return err
	}

	image := config.Image
	if image == "" {
		image, _, err = buildRedisDockerImage(engine, version)
		if err != nil {
			return err
		}
	}

	password, _ := config.Metadata["password"].(string)

	// Build redis-server command args
	cmdArgs := buildRedisCommandArgs(engine, config.Metadata, password)

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
	healthTest := []string{"CMD", redisCLIName(engine)}
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

func resolveRedisEngine(metadata map[string]interface{}) (string, error) {
	engine, ok := metadata["engine"].(string)
	if !ok {
		engine = ""
	}
	engine = strings.ToLower(strings.TrimSpace(engine))
	if engine == "" {
		return defaultRedisEngine, nil
	}
	switch engine {
	case "redis", "valkey":
		return engine, nil
	default:
		return "", fmt.Errorf("unsupported redis engine %q (must be redis or valkey)", engine)
	}
}

func buildRedisDockerImage(engine, version string) (string, string, error) {
	switch engine {
	case "valkey":
		if version == "" {
			version = defaultValkeyVersion
		}
		return fmt.Sprintf("valkey/valkey:%s-alpine", version), version, nil
	case "redis":
		if version == "" {
			version = defaultRedisVersion
		}
		return fmt.Sprintf("redis:%s-alpine", version), version, nil
	default:
		return "", "", fmt.Errorf("unsupported redis engine %q (must be redis or valkey)", engine)
	}
}

// provisionNative provisions Redis using Ansible.
func (r *RedisProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, instanceName string) error {
	fmt.Printf("Provisioning Redis instance %q in native mode...\n", instanceName)

	password, _ := config.Metadata["password"].(string)
	port := config.Port
	if port == 0 {
		port = 6379
	}

	engine, err := resolveRedisEngine(config.Metadata)
	if err != nil {
		return err
	}
	family, err := r.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("detect distro family: %w", err)
	}

	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	playbook := GenerateRedisPlaybook(hostID, engine, instanceName, port, password, family, config.Metadata)

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    hostID,
		Address: host.ExternalIP,
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
	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("redis health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for Redis.
func (r *RedisProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

// buildRedisCommandArgs constructs server CLI flags from metadata.
func buildRedisCommandArgs(engine string, metadata map[string]interface{}, password string) string {
	args := []string{redisServerName(engine), "--appendonly", "yes"}

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

func redisServerName(engine string) string {
	if engine == "valkey" {
		return "valkey-server"
	}
	return "redis-server"
}

func redisCLIName(engine string) string {
	if engine == "valkey" {
		return "valkey-cli"
	}
	return "redis-cli"
}

// appendComposeCommand injects a command directive into generated compose YAML.
func appendComposeCommand(composeYAML, serviceName, command string) string {
	// Insert command after the image line
	target := fmt.Sprintf("    container_name: frameworks-%s\n    restart: always", serviceName)
	replacement := fmt.Sprintf("    container_name: frameworks-%s\n    restart: always\n    command: %s", serviceName, command)
	return strings.Replace(composeYAML, target, replacement, 1)
}

type redisNativeSpec struct {
	engine       string
	packageName  string
	serviceUser  string
	serviceGroup string
	configDir    string
	configPath   string
	dataDir      string
	serverBinary string
	serviceName  string
	serviceLabel string
	installTask  string
	configTask   string
	dataDirTask  string
	systemdTask  string
	enableTask   string
}

func buildRedisNativeSpec(engine, instanceName, family string) redisNativeSpec {
	packageName := "redis"
	if family == "debian" {
		packageName = "redis-server"
	}
	spec := redisNativeSpec{
		engine:       engine,
		serviceName:  fmt.Sprintf("frameworks-redis-%s", instanceName),
		serviceLabel: "Redis",
		packageName:  packageName,
		serviceUser:  "redis",
		serviceGroup: "redis",
		configDir:    "/etc/redis",
		configPath:   fmt.Sprintf("/etc/redis/redis-%s.conf", instanceName),
		dataDir:      fmt.Sprintf("/var/lib/frameworks/redis-%s", instanceName),
		serverBinary: "/usr/bin/redis-server",
		installTask:  "Install Redis server",
		configTask:   "Write Redis configuration",
		dataDirTask:  "Create Redis data directory",
		systemdTask:  "Create systemd unit for Redis",
		enableTask:   "Enable Redis service",
	}

	if engine == "valkey" {
		spec.serviceLabel = "Valkey"
		spec.packageName = "valkey"
		if family == "debian" {
			spec.packageName = "valkey-server"
		}
		spec.serviceUser = "valkey"
		spec.serviceGroup = "valkey"
		spec.configDir = "/etc/valkey"
		spec.configPath = fmt.Sprintf("/etc/valkey/valkey-%s.conf", instanceName)
		spec.dataDir = fmt.Sprintf("/var/lib/frameworks/valkey-%s", instanceName)
		spec.serverBinary = "/usr/bin/valkey-server"
		spec.installTask = "Install Valkey server"
		spec.configTask = "Write Valkey configuration"
		spec.dataDirTask = "Create Valkey data directory"
		spec.systemdTask = "Create systemd unit for Valkey"
		spec.enableTask = "Enable Valkey service"
	}

	return spec
}

// GenerateRedisPlaybook creates an Ansible playbook for native Redis or Valkey installation.
func GenerateRedisPlaybook(host, engine, instanceName string, port int, password, family string, metadata map[string]interface{}) *ansible.Playbook {
	playbook := ansible.NewPlaybook("Provision Redis", host)
	spec := buildRedisNativeSpec(engine, instanceName, family)

	// Build redis.conf directives
	configLines := []string{
		fmt.Sprintf("port %d", port),
		"bind 0.0.0.0",
		"appendonly yes",
		"daemonize no",
		fmt.Sprintf("dir %s", spec.dataDir),
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

	play := ansible.Play{
		Name:        fmt.Sprintf("Install and configure %s instance %s", spec.serviceLabel, instanceName),
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []ansible.Task{
			{
				Name:   spec.installTask,
				Module: "package",
				Args: map[string]interface{}{
					"name":  spec.packageName,
					"state": "present",
				},
			},
			{
				Name:   "Create native config directory",
				Module: "file",
				Args: map[string]interface{}{
					"path":  spec.configDir,
					"state": "directory",
					"owner": spec.serviceUser,
					"group": spec.serviceGroup,
					"mode":  "0755",
				},
			},
			{
				Name:   spec.dataDirTask,
				Module: "file",
				Args: map[string]interface{}{
					"path":  spec.dataDir,
					"state": "directory",
					"owner": spec.serviceUser,
					"group": spec.serviceGroup,
					"mode":  "0750",
				},
			},
			{
				Name:   spec.configTask,
				Module: "copy",
				Args: map[string]interface{}{
					"content": configContent,
					"dest":    spec.configPath,
					"owner":   spec.serviceUser,
					"group":   spec.serviceGroup,
					"mode":    "0640",
				},
				Notify: []string{fmt.Sprintf("restart %s", spec.serviceName)},
			},
			{
				Name:   spec.systemdTask,
				Module: "copy",
				Args: map[string]interface{}{
					"content": generateRedisSystemdUnit(spec, instanceName),
					"dest":    fmt.Sprintf("/etc/systemd/system/%s.service", spec.serviceName),
					"mode":    "0644",
				},
				Notify: []string{"reload systemd", fmt.Sprintf("restart %s", spec.serviceName)},
			},
			{
				Name:   spec.enableTask,
				Module: "systemd",
				Args: map[string]interface{}{
					"name":    spec.serviceName,
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
				Name:   fmt.Sprintf("restart %s", spec.serviceName),
				Module: "systemd",
				Args: map[string]interface{}{
					"name":  spec.serviceName,
					"state": "restarted",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// generateRedisSystemdUnit creates a systemd unit file for a Redis instance.
func generateRedisSystemdUnit(spec redisNativeSpec, instanceName string) string {
	return fmt.Sprintf(`[Unit]
Description=Frameworks %s (%s)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
ExecStart=%s %s
Restart=always
RestartSec=5s
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`, spec.serviceLabel, instanceName, spec.serviceUser, spec.serviceGroup, spec.serverBinary, spec.configPath)
}
