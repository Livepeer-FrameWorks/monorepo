package provisioner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// KafkaProvisioner provisions Kafka brokers (combined or broker-only mode).
type KafkaProvisioner struct {
	*BaseProvisioner
	executor *ansible.Executor
}

// NewKafkaProvisioner creates a new Kafka provisioner for broker tasks.
func NewKafkaProvisioner(pool *ssh.Pool) (*KafkaProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("failed to create ansible executor: %w", err)
	}

	return &KafkaProvisioner{
		BaseProvisioner: NewBaseProvisioner("kafka", pool),
		executor:        executor,
	}, nil
}

// NewKafkaControllerProvisioner creates a Kafka provisioner for dedicated controller tasks.
func NewKafkaControllerProvisioner(pool *ssh.Pool) (*KafkaProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("failed to create ansible executor: %w", err)
	}

	return &KafkaProvisioner{
		BaseProvisioner: NewBaseProvisioner("kafka-controller", pool),
		executor:        executor,
	}, nil
}

// Detect checks if Kafka is installed and running.
func (k *KafkaProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return k.CheckExists(ctx, host, k.GetName())
}

// Provision installs Kafka using Ansible (dispatches by role). Runs every
// apply — each task's idempotence gate handles reruns on healthy hosts, and
// config/unit drift gets reconciled.
func (k *KafkaProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	role, _ := config.Metadata["role"].(string) //nolint:errcheck // metadata validated by schema
	switch role {
	case "controller":
		return k.provisionController(ctx, host, config)
	case "broker":
		return k.provisionBroker(ctx, host, config)
	default:
		return k.provisionCombined(ctx, host, config)
	}
}

func (k *KafkaProvisioner) provisionController(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	nodeID, ok := config.Metadata["broker_id"].(int)
	if !ok {
		return fmt.Errorf("broker_id (node.id) not found in config")
	}

	clusterID, _ := config.Metadata["cluster_id"].(string)                   //nolint:errcheck // metadata validated by schema
	bootstrapServers, _ := config.Metadata["bootstrap_servers"].(string)     //nolint:errcheck // metadata validated by schema
	initialControllers, _ := config.Metadata["initial_controllers"].(string) //nolint:errcheck // metadata validated by schema

	err := validateApacheKafkaVersion(config.Version)
	if err != nil {
		return err
	}

	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	controllerPort := config.Port
	if controllerPort == 0 {
		controllerPort = 9093
	}

	_, remoteArch, err := k.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("detect remote arch: %w", err)
	}
	artifact, err := resolveInfraArtifactFromChannel("kafka", "linux-"+remoteArch, platformChannelFromMetadata(config.Metadata), config.Metadata)
	if err != nil {
		return err
	}
	javaSpec, err := k.resolveJavaSpec(ctx, host)
	if err != nil {
		return err
	}

	playbook := ansible.GenerateKafkaControllerPlaybook(config.Version, nodeID, hostID, controllerPort, bootstrapServers, clusterID, initialControllers, artifact.URL, artifact.Checksum, javaSpec)
	return k.executePlaybook(ctx, host, playbook)
}

func (k *KafkaProvisioner) provisionBroker(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	nodeID, ok := config.Metadata["broker_id"].(int)
	if !ok {
		return fmt.Errorf("broker_id (node.id) not found in config")
	}

	clusterID, _ := config.Metadata["cluster_id"].(string)               //nolint:errcheck // metadata validated by schema
	bootstrapServers, _ := config.Metadata["bootstrap_servers"].(string) //nolint:errcheck // metadata validated by schema

	err := validateApacheKafkaVersion(config.Version)
	if err != nil {
		return err
	}

	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	port := config.Port
	if port == 0 {
		port = 9092
	}

	_, remoteArch, err := k.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("detect remote arch: %w", err)
	}
	artifact, err := resolveInfraArtifactFromChannel("kafka", "linux-"+remoteArch, platformChannelFromMetadata(config.Metadata), config.Metadata)
	if err != nil {
		return err
	}
	javaSpec, err := k.resolveJavaSpec(ctx, host)
	if err != nil {
		return err
	}

	playbook := ansible.GenerateKafkaBrokerPlaybook(config.Version, nodeID, hostID, port, bootstrapServers, clusterID, config.Metadata, artifact.URL, artifact.Checksum, javaSpec)
	return k.executePlaybook(ctx, host, playbook)
}

func (k *KafkaProvisioner) provisionCombined(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	brokerID, ok := config.Metadata["broker_id"].(int)
	if !ok {
		return fmt.Errorf("broker_id not found in config")
	}

	clusterID, _ := config.Metadata["cluster_id"].(string)                      //nolint:errcheck // metadata validated by schema
	controllerQuorum, _ := config.Metadata["controller_quorum_voters"].(string) //nolint:errcheck // metadata validated by schema
	controllerPort, _ := config.Metadata["controller_port"].(int)               //nolint:errcheck // zero value handled below
	if controllerPort == 0 {
		controllerPort = 9093
	}

	err := validateApacheKafkaVersion(config.Version)
	if err != nil {
		return err
	}

	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	port := config.Port
	if port == 0 {
		port = 9092
	}

	_, remoteArch, err := k.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("detect remote arch: %w", err)
	}
	artifact, err := resolveInfraArtifactFromChannel("kafka", "linux-"+remoteArch, platformChannelFromMetadata(config.Metadata), config.Metadata)
	if err != nil {
		return err
	}
	javaSpec, err := k.resolveJavaSpec(ctx, host)
	if err != nil {
		return err
	}

	playbook := ansible.GenerateKafkaKRaftPlaybook(config.Version, brokerID, hostID, port, controllerPort, controllerQuorum, clusterID, config.Metadata, artifact.URL, artifact.Checksum, javaSpec)
	return k.executePlaybook(ctx, host, playbook)
}

// resolveJavaSpec detects the host's distro family and returns the matching
// JRE package spec, erroring if the family isn't in JavaRuntimePackages.
func (k *KafkaProvisioner) resolveJavaSpec(ctx context.Context, host inventory.Host) (ansible.DistroPackageSpec, error) {
	family, err := k.DetectDistroFamily(ctx, host)
	if err != nil {
		return ansible.DistroPackageSpec{}, fmt.Errorf("detect distro family: %w", err)
	}
	spec, ok := ansible.ResolveDistroPackage(ansible.JavaRuntimePackages, family)
	if !ok {
		return ansible.DistroPackageSpec{}, fmt.Errorf("kafka: unsupported distro family %q for Java install", family)
	}
	return spec, nil
}

func (k *KafkaProvisioner) executePlaybook(ctx context.Context, host inventory.Host, playbook *ansible.Playbook) error {
	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    hostID,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": k.sshPool.DefaultKeyPath(),
		},
	})

	opts := ansible.ExecuteOptions{
		Verbose: true,
	}

	result, err := k.executor.ExecutePlaybook(ctx, playbook, inv, opts)
	if err != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", err, result.Output)
	}

	if !result.Success {
		return fmt.Errorf("ansible playbook failed\nOutput: %s", result.Output)
	}

	return nil
}

func validateApacheKafkaVersion(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if strings.HasPrefix(version, "7.") {
		return fmt.Errorf("kafka native mode expects an Apache Kafka version (3.x or 4.x); got Confluent version %q", version)
	}
	return nil
}

// Validate checks Kafka structural state via goss, then uses a TCP check for
// controllers and a broker protocol check for broker listeners.
func (k *KafkaProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	role, _ := config.Metadata["role"].(string) //nolint:errcheck // metadata validated by schema

	if _, remoteArch, err := k.DetectRemoteArch(ctx, host); err == nil {
		unit := "frameworks-kafka"
		filePresent := "/opt/kafka/bin/kafka-server-start.sh"
		if role == "controller" {
			unit = "frameworks-kafka-controller"
		}
		spec := ansible.RenderGossYAML(ansible.GossSpec{
			Services: map[string]ansible.GossService{
				unit: {Running: true, Enabled: true},
			},
			Files: map[string]ansible.GossFile{
				filePresent: {Exists: true},
			},
		})
		if gossErr := runGossValidate(ctx, k.executor, k.sshPool.DefaultKeyPath(), host,
			"kafka-"+role, platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
			return fmt.Errorf("kafka goss validate failed: %w", gossErr)
		}
	}

	clusterIP := host.ExternalIP
	if clusterIP == "" {
		clusterIP = "127.0.0.1"
	}

	// Controllers speak only the KRaft peer protocol; no broker handshake
	// a client can do. wait_for on the cluster IP + port handles all the
	// 0.0.0.0 / [::] / explicit-IP binding variations the kernel exposes,
	// so we don't need a brittle ss-text regex.
	if role == "controller" {
		tasks := []ansible.Task{
			waitForTCP("wait for kafka controller", clusterIP, config.Port, 30),
		}
		return runValidatePlaybook(ctx, k.executor, k.sshPool.DefaultKeyPath(), host, "kafka-controller", tasks)
	}

	// Broker: bundled CLI tool against the cluster-facing bootstrap address.
	// Exercises the wire protocol AND the advertised-listener binding.
	tasks := []ansible.Task{
		waitForTCP("wait for kafka broker", clusterIP, config.Port, 30),
		commandOK("kafka broker api versions",
			"/opt/kafka/bin/kafka-broker-api-versions.sh",
			"--bootstrap-server", fmt.Sprintf("%s:%d", clusterIP, config.Port)),
	}
	return runValidatePlaybook(ctx, k.executor, k.sshPool.DefaultKeyPath(), host, "kafka-broker", tasks)
}

// Initialize creates Kafka topics (broker-only; controllers skip this). Runs
// /opt/kafka/bin/kafka-topics.sh on the broker host via SSH so the CLI never
// needs to reach the broker port directly. --if-exists / --if-not-exists
// gates keep reruns idempotent.
func (k *KafkaProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	role, _ := config.Metadata["role"].(string) //nolint:errcheck // metadata validated by schema
	if role == "controller" {
		return nil
	}

	topicsConfig, ok := config.Metadata["topics"].([]map[string]any)
	if !ok {
		fmt.Println("No topics to create")
		return nil
	}

	clusterIP := host.ExternalIP
	if clusterIP == "" {
		clusterIP = "127.0.0.1"
	}
	bootstrap := fmt.Sprintf("%s:%d", clusterIP, config.Port)
	topicsBin := "/opt/kafka/bin/kafka-topics.sh"

	tasks := make([]ansible.Task, 0, len(topicsConfig))
	for _, topicCfg := range topicsConfig {
		name, _ := topicCfg["name"].(string)                   //nolint:errcheck // schema-validated
		partitions, _ := topicCfg["partitions"].(int)          //nolint:errcheck
		replication, _ := topicCfg["replication_factor"].(int) //nolint:errcheck

		argv := []string{topicsBin,
			"--bootstrap-server", bootstrap,
			"--create", "--if-not-exists",
			"--topic", name,
			"--partitions", fmt.Sprintf("%d", partitions),
			"--replication-factor", fmt.Sprintf("%d", replication),
		}
		if cfg, ok := topicCfg["config"].(map[string]any); ok {
			keys := make([]string, 0, len(cfg))
			for key := range cfg {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				argv = append(argv, "--config", fmt.Sprintf("%s=%v", key, cfg[key]))
			}
		}
		tasks = append(tasks, commandOK("create kafka topic "+name, argv...))
	}

	if err := runValidatePlaybook(ctx, k.executor, k.sshPool.DefaultKeyPath(), host, "kafka-topics", tasks); err != nil {
		return fmt.Errorf("failed to create kafka topics: %w", err)
	}
	return nil
}
