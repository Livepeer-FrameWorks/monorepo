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

const defaultZookeeperImage = "confluentinc/cp-zookeeper:7.4.0"
const defaultApacheZookeeperVersion = "3.9.2"

// ZookeeperProvisioner provisions Zookeeper nodes.
type ZookeeperProvisioner struct {
	*BaseProvisioner
	executor *ansible.Executor
}

// NewZookeeperProvisioner creates a new Zookeeper provisioner.
func NewZookeeperProvisioner(pool *ssh.Pool) (*ZookeeperProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("create ansible executor: %w", err)
	}
	return &ZookeeperProvisioner{
		BaseProvisioner: NewBaseProvisioner("zookeeper", pool),
		executor:        executor,
	}, nil
}

// Detect checks if Zookeeper is installed and running.
func (z *ZookeeperProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return z.CheckExists(ctx, host, "zookeeper")
}

// Provision installs Zookeeper using Docker or native systemd. Runs every
// apply — each task's idempotence gate handles reruns on healthy hosts, and
// version-keyed install sentinels make upgrades trigger re-extraction.
func (z *ZookeeperProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 2181
	}

	switch config.Mode {
	case "docker":
		return z.provisionDocker(ctx, host, config, port)
	case "native":
		if err := validateApacheZookeeperVersion(config.Version); err != nil {
			return err
		}
		return z.provisionNative(ctx, host, config, port)
	default:
		return fmt.Errorf("unsupported zookeeper mode %q (must be docker or native)", config.Mode)
	}
}

func (z *ZookeeperProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, port int) error {

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
	if writeErr := os.WriteFile(tmpEnvFile, []byte(envFileContent), 0600); writeErr != nil {
		return writeErr
	}
	defer os.Remove(tmpEnvFile)

	remoteEnvFile := "/etc/frameworks/zookeeper.env"
	if uploadErr := z.UploadFile(ctx, host, ssh.UploadOptions{LocalPath: tmpEnvFile, RemotePath: remoteEnvFile, Mode: 0600}); uploadErr != nil {
		return uploadErr
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

	composeCmd := "cd /opt/frameworks/zookeeper && docker compose pull && docker compose up -d"
	result, err := z.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	return nil
}

// BuildZookeeperConfig returns the /etc/zookeeper/zoo.cfg bytes.
func BuildZookeeperConfig(port int, serverLines string) []byte {
	trailer := ""
	if serverLines != "" {
		trailer = serverLines + "\n"
	}
	return fmt.Appendf(nil, `tickTime=2000
initLimit=10
syncLimit=5
dataDir=/var/lib/zookeeper/data
dataLogDir=/var/lib/zookeeper/log
clientPort=%d
autopurge.snapRetainCount=3
autopurge.purgeInterval=24
%s`, port, trailer)
}

// zookeeperSystemdUnitSpec returns the SystemdUnitSpec for the zookeeper service.
func zookeeperSystemdUnitSpec() ansible.SystemdUnitSpec {
	return ansible.SystemdUnitSpec{
		Description: "FrameWorks ZooKeeper",
		After:       []string{"network-online.target"},
		Wants:       []string{"network-online.target"},
		User:        "zookeeper",
		Group:       "zookeeper",
		Environment: map[string]string{"ZOO_LOG_DIR": "/var/lib/zookeeper/log"},
		ExecStart:   "/opt/zookeeper/bin/zkServer.sh start-foreground /etc/zookeeper/zoo.cfg",
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "65535",
	}
}

// BuildZookeeperSystemdUnit returns the frameworks-zookeeper.service bytes.
func BuildZookeeperSystemdUnit() []byte {
	return []byte(ansible.RenderSystemdUnit(zookeeperSystemdUnitSpec()))
}

func (z *ZookeeperProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, port int) error {
	_ = resolveZookeeperNativeVersion(config.Version) // retained for validation side-effect of the helper
	serverID := zookeeperServerID(config.Metadata["server_id"])
	serverLines := strings.Join(zookeeperServerList(config.Metadata["servers"]), "\n")

	_, remoteArch, err := z.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("detect remote arch: %w", err)
	}
	archKey := "linux-" + remoteArch
	artifact, err := resolveInfraArtifactFromChannel("zookeeper", archKey, platformChannelFromMetadata(config.Metadata), config.Metadata)
	if err != nil {
		return err
	}
	family, err := z.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("detect distro family: %w", err)
	}
	javaSpec, ok := ansible.ResolveDistroPackage(ansible.JavaRuntimePackages, family)
	if !ok {
		return fmt.Errorf("zookeeper: unsupported distro family %q for Java install", family)
	}

	tasks := zookeeperProvisionTasks(port, serverID, serverLines, artifact.URL, artifact.Checksum, javaSpec)

	playbook := &ansible.Playbook{
		Name:  "Provision ZooKeeper",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Provision ZooKeeper node",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
				Tasks:       tasks,
			},
		},
	}

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": z.sshPool.DefaultKeyPath(),
		},
	})

	result, execErr := z.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("ansible playbook failed\nOutput: %s", result.Output)
	}

	return nil
}

// zookeeperProvisionTasks renders the declarative task list for a ZooKeeper
// node. Archive contract: the vendor tarball wraps a single
// `apache-zookeeper-<version>-bin/` top directory; --strip-components=1 drops
// it and lands bin/, lib/, conf/ directly under /opt/zookeeper.
func zookeeperProvisionTasks(port, serverID int, serverLines, artifactURL, artifactChecksum string, javaSpec ansible.DistroPackageSpec) []ansible.Task {
	zooCfg := string(BuildZookeeperConfig(port, serverLines))
	unit := ansible.RenderSystemdUnit(zookeeperSystemdUnitSpec())
	installSentinel := ansible.ArtifactSentinel("/opt/zookeeper", artifactChecksum+artifactURL)

	tasks := []ansible.Task{
		// curl + Java runtime prerequisites, both idempotent via the package
		// module. Distro-aware Java name comes from JavaRuntimePackages; a
		// pre-existing Java >= 11 already satisfies the runtime requirement so
		// installing the pinned OpenJDK only introduces a dormant provider
		// on Arch (archlinux-java can switch between them).
		ansible.TaskPackage("curl", ansible.PackagePresent),
		ansible.TaskPackage(javaSpec.PackageName, ansible.PackagePresent),

		// zookeeper user + group.
		{
			Name:   "ensure zookeeper group",
			Module: "ansible.builtin.group",
			Args:   map[string]any{"name": "zookeeper", "system": true, "state": "present"},
		},
		{
			Name:   "ensure zookeeper user",
			Module: "ansible.builtin.user",
			Args: map[string]any{
				"name":   "zookeeper",
				"group":  "zookeeper",
				"system": true,
				"shell":  "/usr/sbin/nologin",
				"state":  "present",
			},
		},

		// Directory layout.
		{
			Name:   "create /etc/zookeeper",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/zookeeper", "state": "directory", "owner": "zookeeper", "group": "zookeeper", "mode": "0755"},
		},
		{
			Name:   "create /var/lib/zookeeper/data",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/var/lib/zookeeper/data", "state": "directory", "owner": "zookeeper", "group": "zookeeper", "mode": "0755"},
		},
		{
			Name:   "create /var/lib/zookeeper/log",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/var/lib/zookeeper/log", "state": "directory", "owner": "zookeeper", "group": "zookeeper", "mode": "0755"},
		},
		{
			Name:   "create /opt/zookeeper",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/opt/zookeeper", "state": "directory", "owner": "zookeeper", "group": "zookeeper", "mode": "0755"},
		},

		// Download + extract tarball. strip-components drops the versioned top
		// dir. Version-keyed sentinel: a pinned-version bump rotates the
		// marker path, triggering unarchive + touch to re-extract on top.
		// Tarball stays in /tmp so get_url cache-hits on same-version reruns.
		ansible.TaskGetURL(artifactURL, "/tmp/zookeeper.tgz", artifactChecksum),
		ansible.TaskUnarchive("/tmp/zookeeper.tgz", "/opt/zookeeper", installSentinel,
			ansible.UnarchiveOpts{StripComponents: 1, Owner: "zookeeper", Group: "zookeeper"}),
		ansible.TaskShell("touch "+installSentinel+" && chown zookeeper:zookeeper "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel}),

		// Config + unit file.
		ansible.TaskCopy("/etc/zookeeper/zoo.cfg", zooCfg, ansible.CopyOpts{Owner: "zookeeper", Group: "zookeeper", Mode: "0644"}),
		ansible.TaskCopy("/etc/systemd/system/frameworks-zookeeper.service", unit, ansible.CopyOpts{Mode: "0644"}),
	}

	// myid only when an explicit non-zero server_id is configured.
	if serverID > 0 {
		tasks = append(tasks, ansible.TaskCopy(
			"/var/lib/zookeeper/data/myid",
			fmt.Sprintf("%d\n", serverID),
			ansible.CopyOpts{Owner: "zookeeper", Group: "zookeeper", Mode: "0644"},
		))
	}

	tasks = append(tasks,
		ansible.TaskSystemdService("frameworks-zookeeper", ansible.SystemdOpts{
			State:        "started",
			Enabled:      ansible.BoolPtr(true),
			DaemonReload: true,
		}),
	)

	return tasks
}

// Validate checks Zookeeper structural state via goss, then the TCP listener.
func (z *ZookeeperProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if _, remoteArch, err := z.DetectRemoteArch(ctx, host); err == nil {
		spec := ansible.RenderGossYAML(ansible.GossSpec{
			Services: map[string]ansible.GossService{
				"frameworks-zookeeper": {Running: true, Enabled: true},
			},
			Ports: map[string]ansible.GossPort{
				fmt.Sprintf("tcp:%d", config.Port): {Listening: true},
			},
			Files: map[string]ansible.GossFile{
				"/opt/zookeeper/bin/zkServer.sh": {Exists: true},
			},
		})
		if gossErr := runGossValidate(ctx, z.executor, z.sshPool.DefaultKeyPath(), host,
			"zookeeper", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
			return fmt.Errorf("zookeeper goss validate failed: %w", gossErr)
		}
	}

	checker := &health.TCPChecker{}
	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("zookeeper health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for Zookeeper.
func (z *ZookeeperProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

func resolveZookeeperNativeVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return defaultApacheZookeeperVersion
	}
	return version
}

func validateApacheZookeeperVersion(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if strings.HasPrefix(version, "7.") {
		return fmt.Errorf("zookeeper native mode expects an Apache ZooKeeper version such as 3.9.2; got %q", version)
	}
	return nil
}

func zookeeperServerID(raw any) int {
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func zookeeperServerList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, value)
			}
		}
		return out
	default:
		return nil
	}
}
