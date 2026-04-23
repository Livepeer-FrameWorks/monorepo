package provisioner

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// FlexibleProvisioner provisions services in either Docker or native mode
type FlexibleProvisioner struct {
	*BaseProvisioner
	serviceName string
	port        int
	healthPath  string
	executor    *ansible.Executor
}

// NewFlexibleProvisioner creates a new flexible provisioner
func NewFlexibleProvisioner(serviceName string, port int, pool *ssh.Pool) *FlexibleProvisioner {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		panic(fmt.Sprintf("create ansible executor for %s: %v", serviceName, err))
	}
	return &FlexibleProvisioner{
		BaseProvisioner: NewBaseProvisioner(serviceName, pool),
		serviceName:     serviceName,
		port:            port,
		healthPath:      "/health",
		executor:        executor,
	}
}

// Detect checks if the service exists
func (f *FlexibleProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return f.CheckExists(ctx, host, f.serviceName)
}

// Provision runs the docker or native install playbook on every apply.
// Idempotence is the tasks' responsibility: get_url skips on checksum match,
// unarchive skips on its version-keyed sentinel, and systemd_service
// state=started is a no-op on an already-running unit. Skipping in Go would
// mask config/unit drift between applies.
func (f *FlexibleProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "docker" && config.Image != "" {
		return f.provisionDocker(ctx, host, config, &gitops.ServiceInfo{FullImage: config.Image})
	}
	if config.Mode == "native" && config.BinaryURL != "" {
		return f.provisionNative(ctx, host, config, &gitops.ServiceInfo{Binaries: map[string]gitops.Artifact{"*": {URL: config.BinaryURL}}})
	}

	channel, version := gitops.ResolveVersion(config.Version)
	manifest, err := fetchGitopsManifest(channel, version, config.Metadata)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest: %w", err)
	}
	svcInfo, err := manifest.GetServiceInfo(f.serviceName)
	if err != nil {
		return fmt.Errorf("service not found in manifest: %w", err)
	}

	switch config.Mode {
	case "docker":
		return f.provisionDocker(ctx, host, config, svcInfo)
	case "native":
		return f.provisionNative(ctx, host, config, svcInfo)
	default:
		return fmt.Errorf("unsupported mode: %s (must be docker or native)", config.Mode)
	}
}

// provisionDocker provisions the service using Docker
func (f *FlexibleProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in Docker mode...\n", f.serviceName)

	port := f.port
	if config.Port != 0 {
		port = config.Port
	}

	// Write env file to remote host before compose references it
	svcEnvFile := fmt.Sprintf("/etc/frameworks/%s.env", f.serviceName)
	if err := f.writeServiceEnvFile(ctx, host, svcEnvFile, config); err != nil {
		fmt.Printf("    Warning: could not write env file %s: %v\n", svcEnvFile, err)
	}

	// Generate docker-compose.yml
	envFile := config.EnvFile
	if envFile == "" {
		envFile = svcEnvFile
	}

	envVars := maps.Clone(config.EnvVars)
	if envVars == nil {
		envVars = map[string]string{}
	}
	if len(envVars) == 0 {
		if clusterID, ok := config.Metadata["cluster_id"].(string); ok && clusterID != "" {
			envVars["CLUSTER_ID"] = clusterID
		}
		if nodeID, ok := config.Metadata["node_id"].(string); ok && nodeID != "" {
			envVars["NODE_ID"] = nodeID
		}
	}

	composeData := DockerComposeData{
		ServiceName: f.serviceName,
		Image:       svcInfo.FullImage, // image@sha256:digest format
		Port:        port,
		EnvFile:     envFile,
		Environment: envVars,
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", fmt.Sprintf("http://localhost:%d%s", port, f.healthPath)},
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Volumes: []string{
			fmt.Sprintf("/var/log/frameworks/%s:/var/log/frameworks", f.serviceName),
			fmt.Sprintf("/var/lib/frameworks/%s:/var/lib/frameworks", f.serviceName),
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	// Create local temp file
	tmpDir, err := os.MkdirTemp("", f.serviceName+"-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	// Upload to host
	remotePath := fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", f.serviceName)
	if err := f.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remotePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	// Pull and start with docker compose
	composeCmd := fmt.Sprintf("cd /opt/frameworks/%s && docker compose pull", f.serviceName)
	if !config.DeferStart {
		composeCmd += " && docker compose up -d"
	}
	result, err := f.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	if config.DeferStart {
		fmt.Printf("⏸ %s deployed but NOT started (missing required config)\n", f.serviceName)
	} else {
		fmt.Printf("✓ %s provisioned in Docker mode\n", f.serviceName)
	}
	return nil
}

// provisionNative installs the service as a native binary with systemd via a
// declarative Ansible playbook. Idempotence:
//   - get_url skips on checksum match;
//   - unarchive skips on a version-keyed sentinel (rotates per URL+checksum);
//   - TaskCopy of env file + unit file is byte-for-byte idempotent;
//   - TaskSystemdService is a no-op once the unit is started.
//
// Same-version reruns are changed=0; a pinned-version bump rotates the
// sentinel and re-extracts the new binary.
func (f *FlexibleProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", f.serviceName)

	var (
		binaryURL = config.BinaryURL
		checksum  string
	)
	if binaryURL == "" {
		if v, ok := svcInfo.Binaries["*"]; ok && v.URL != "" {
			binaryURL, checksum = v.URL, v.Checksum
		}
	}
	if binaryURL == "" {
		remoteOS, remoteArch, archErr := f.DetectRemoteArch(ctx, host)
		if archErr != nil {
			return fmt.Errorf("failed to detect remote architecture: %w", archErr)
		}
		bin, binErr := svcInfo.GetBinary(remoteOS, remoteArch)
		if binErr != nil {
			return fmt.Errorf("binary not available: %w", binErr)
		}
		binaryURL, checksum = bin.URL, bin.Checksum
	}

	installDir := fmt.Sprintf("/opt/frameworks/%s", f.serviceName)
	assetPath := fmt.Sprintf("/tmp/%s.asset", f.serviceName)
	envFilePath := fmt.Sprintf("/etc/frameworks/%s.env", f.serviceName)
	unitPath := fmt.Sprintf("/etc/systemd/system/frameworks-%s.service", f.serviceName)
	installSentinel := ansible.ArtifactSentinel(installDir, checksum+binaryURL)

	isZip := strings.HasSuffix(binaryURL, ".zip")

	envContent := string(BuildServiceEnvFileBytes(config))
	unitContent := ansible.RenderSystemdUnit(ansible.SystemdUnitSpec{
		Description:     fmt.Sprintf("Frameworks %s", f.serviceName),
		After:           []string{"network-online.target"},
		Wants:           []string{"network-online.target"},
		User:            "frameworks",
		WorkingDir:      installDir,
		EnvironmentFile: envFilePath,
		ExecStart:       fmt.Sprintf("%s/%s", installDir, f.serviceName),
		Restart:         "always",
		RestartSec:      5,
	})

	tasks := []ansible.Task{
		mkdirTask("/etc/frameworks", "frameworks", "frameworks", "0755"),
		mkdirTask(installDir, "frameworks", "frameworks", "0755"),
	}
	if isZip {
		tasks = append(tasks, ansible.TaskPackage("unzip", ansible.PackagePresent))
	}
	if envContent != "" {
		tasks = append(tasks, ansible.TaskCopy(envFilePath, envContent, ansible.CopyOpts{
			Owner: "frameworks", Group: "frameworks", Mode: "0600",
		}))
	}
	tasks = append(tasks,
		ansible.TaskGetURL(binaryURL, assetPath, checksum),
		ansible.TaskUnarchive(assetPath, installDir, installSentinel,
			ansible.UnarchiveOpts{Owner: "frameworks", Group: "frameworks"}),
		ansible.TaskShell(
			// Vendor releases use one of three filename conventions inside the
			// archive; pick whichever shipped, then mark the sentinel.
			fmt.Sprintf(
				"mv %[1]s/frameworks-%[2]s-* %[1]s/%[2]s 2>/dev/null || "+
					"mv %[1]s/%[2]s %[1]s/%[2]s 2>/dev/null || "+
					"mv %[1]s/frameworks %[1]s/%[2]s 2>/dev/null || true; "+
					"chmod +x %[1]s/%[2]s; touch %[3]s; chown frameworks:frameworks %[3]s",
				installDir, f.serviceName, installSentinel,
			),
			ansible.ShellOpts{Creates: installSentinel},
		),
		ansible.TaskCopy(unitPath, unitContent, ansible.CopyOpts{Mode: "0644"}),
	)
	if !config.DeferStart {
		tasks = append(tasks, ansible.TaskSystemdService(
			fmt.Sprintf("frameworks-%s", f.serviceName),
			ansible.SystemdOpts{State: "started", Enabled: ansible.BoolPtr(true), DaemonReload: true},
		))
		if f.port > 0 {
			tasks = append(tasks, ansible.TaskWaitForPort(f.port, ansible.WaitForOpts{Timeout: 60, Sleep: 1}))
		}
	} else {
		// DeferStart still needs daemon-reload so the unit file is live; leave
		// start/enable to the operator (or a later apply with DeferStart=false).
		tasks = append(tasks, ansible.Task{
			Name:        "systemd daemon-reload (deferred start)",
			Module:      "ansible.builtin.systemd_service",
			Args:        map[string]any{"daemon_reload": true},
			ChangedWhen: "false",
		})
	}

	playbook := &ansible.Playbook{
		Name:  fmt.Sprintf("Install %s native binary", f.serviceName),
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{{
			Name:        fmt.Sprintf("Install %s", f.serviceName),
			Hosts:       host.ExternalIP,
			Become:      true,
			GatherFacts: false,
			Tasks:       tasks,
		}},
	}
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": f.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := f.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: false})
	if execErr != nil {
		return fmt.Errorf("native install for %s failed: %w\nOutput: %s", f.serviceName, execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("native install playbook for %s failed\nOutput: %s", f.serviceName, result.Output)
	}
	if config.DeferStart {
		fmt.Printf("⏸ %s deployed but NOT started (missing required config)\n", f.serviceName)
	} else {
		fmt.Printf("✓ %s provisioned in native mode\n", f.serviceName)
	}
	return nil
}

// writeServiceEnvFile writes the env-file content from BuildServiceEnvFileBytes
// to the remote host so drift and apply share the exact same bytes. Used by
// docker-mode provisioning; native mode ships the env file via TaskCopy.
func (f *FlexibleProvisioner) writeServiceEnvFile(ctx context.Context, host inventory.Host, envFilePath string, config ServiceConfig) error {
	content := BuildServiceEnvFileBytes(config)
	if len(content) == 0 {
		return nil
	}
	writeCmd := fmt.Sprintf("mkdir -p /etc/frameworks && cat > %s << 'ENVEOF'\n%sENVEOF\nchmod 0600 %s", envFilePath, string(content), envFilePath)
	result, err := f.RunCommand(ctx, host, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write env file: %s", result.Stderr)
	}
	return nil
}

// Validate runs goss structural checks in native mode, then the HTTP health
// probe.
func (f *FlexibleProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "native" {
		if _, remoteArch, err := f.DetectRemoteArch(ctx, host); err == nil {
			gossSpec := ansible.GossSpec{
				Services: map[string]ansible.GossService{
					fmt.Sprintf("frameworks-%s", f.serviceName): {Running: true, Enabled: true},
				},
				Files: map[string]ansible.GossFile{
					fmt.Sprintf("/opt/frameworks/%s/%s", f.serviceName, f.serviceName): {Exists: true},
				},
			}
			if gossErr := runGossValidate(ctx, f.executor, f.sshPool.DefaultKeyPath(), host,
				f.serviceName, platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch,
				ansible.RenderGossYAML(gossSpec)); gossErr != nil {
				return fmt.Errorf("%s goss validate failed: %w", f.serviceName, gossErr)
			}
		}
	}

	if f.port == 0 {
		return nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", f.port, f.healthPath)
	tasks := []ansible.Task{
		waitForTCP("wait for "+f.serviceName+" listener", "127.0.0.1", f.port, 10),
		uriOK(f.serviceName+" /health", url, 200),
	}
	return runValidatePlaybook(ctx, f.executor, f.sshPool.DefaultKeyPath(), host, f.serviceName, tasks)
}

// Initialize is a no-op for most application services
func (f *FlexibleProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Most services don't need initialization
	// (Unlike Postgres/Kafka/ClickHouse which need databases/topics/tables)
	return nil
}
