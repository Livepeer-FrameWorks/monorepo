package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

const defaultZookeeperImage = "confluentinc/cp-zookeeper:7.4.0"

// ZookeeperProvisioner provisions Zookeeper nodes.
type ZookeeperProvisioner struct {
	*BaseProvisioner
}

// NewZookeeperProvisioner creates a new Zookeeper provisioner.
func NewZookeeperProvisioner(pool *ssh.Pool) (*ZookeeperProvisioner, error) {
	return &ZookeeperProvisioner{
		BaseProvisioner: NewBaseProvisioner("zookeeper", pool),
	}, nil
}

// Detect checks if Zookeeper is installed and running.
func (z *ZookeeperProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return z.CheckExists(ctx, host, "zookeeper")
}

// Provision installs Zookeeper using Docker.
func (z *ZookeeperProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := z.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil
	}

	if config.Mode != "docker" {
		return fmt.Errorf("zookeeper provisioner requires docker mode")
	}

	port := config.Port
	if port == 0 {
		port = 2181
	}

	image := config.Image
	if image == "" {
		image = defaultZookeeperImage
	}

	envVars := map[string]string{
		"ZOOKEEPER_CLIENT_PORT": fmt.Sprintf("%d", port),
		"ZOOKEEPER_TICK_TIME":   "2000",
	}

	if serverID, ok := config.Metadata["server_id"].(int); ok && serverID > 0 {
		envVars["ZOOKEEPER_SERVER_ID"] = fmt.Sprintf("%d", serverID)
	}

	if servers, ok := config.Metadata["servers"].([]string); ok && len(servers) > 0 {
		envVars["ZOOKEEPER_SERVERS"] = strings.Join(servers, " ")
	}

	envFileContent := GenerateEnvFile("zookeeper", envVars)
	tmpEnvFile := filepath.Join(os.TempDir(), "zookeeper.env")
	if err := os.WriteFile(tmpEnvFile, []byte(envFileContent), 0600); err != nil {
		return err
	}
	defer os.Remove(tmpEnvFile)

	remoteEnvFile := "/etc/frameworks/zookeeper.env"
	if err := z.UploadFile(ctx, host, ssh.UploadOptions{LocalPath: tmpEnvFile, RemotePath: remoteEnvFile, Mode: 0600}); err != nil {
		return err
	}

	composeData := DockerComposeData{
		ServiceName: "zookeeper",
		Image:       image,
		Port:        port,
		EnvFile:     remoteEnvFile,
		Networks:    []string{"frameworks"},
		Volumes: []string{
			"/var/lib/frameworks/zookeeper:/var/lib/zookeeper",
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "zookeeper-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	if _, err := z.RunCommand(ctx, host, "mkdir -p /opt/frameworks/zookeeper"); err != nil {
		return fmt.Errorf("failed to create remote zookeeper directory: %w", err)
	}

	remotePath := "/opt/frameworks/zookeeper/docker-compose.yml"
	if err := z.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remotePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	commands := []string{
		"cd /opt/frameworks/zookeeper",
		"docker compose pull",
		"docker compose up -d",
	}

	for _, cmd := range commands {
		result, err := z.RunCommand(ctx, host, cmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose command failed: %s\nStderr: %s", cmd, result.Stderr)
		}
	}

	return nil
}

// Validate checks if Zookeeper is healthy.
func (z *ZookeeperProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.TCPChecker{}
	result := checker.Check(host.Address, config.Port)
	if !result.OK {
		return fmt.Errorf("zookeeper health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for Zookeeper.
func (z *ZookeeperProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}
