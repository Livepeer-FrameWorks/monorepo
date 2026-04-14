package provisioner

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
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

// Provision installs Kafka using Ansible (dispatches by role).
func (k *KafkaProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := k.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil
	}

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

	playbook := ansible.GenerateKafkaControllerPlaybook(config.Version, nodeID, hostID, controllerPort, bootstrapServers, clusterID, initialControllers)
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

	playbook := ansible.GenerateKafkaBrokerPlaybook(config.Version, nodeID, hostID, port, bootstrapServers, clusterID, config.Metadata)
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

	playbook := ansible.GenerateKafkaKRaftPlaybook(config.Version, brokerID, hostID, port, controllerPort, controllerQuorum, clusterID, config.Metadata)
	return k.executePlaybook(ctx, host, playbook)
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
			"ansible_ssh_private_key_file": host.SSHKey,
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

// Validate checks if Kafka is healthy.
// Controllers use TCP check (no broker protocol on controller listener).
// Brokers and combined-mode nodes use Sarama broker check.
func (k *KafkaProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	role, _ := config.Metadata["role"].(string) //nolint:errcheck // metadata validated by schema
	if role == "controller" {
		checker := &health.TCPChecker{}
		result := checker.Check(host.ExternalIP, config.Port)
		if !result.OK {
			return fmt.Errorf("kafka controller health check failed: %s", result.Error)
		}
		return nil
	}

	checker := &health.KafkaChecker{}
	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("kafka health check failed: %s", result.Error)
	}

	return nil
}

// Initialize creates Kafka topics (broker-only; controllers skip this).
func (k *KafkaProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	role, _ := config.Metadata["role"].(string) //nolint:errcheck // metadata validated by schema
	if role == "controller" {
		return nil
	}

	topicsConfig, ok := config.Metadata["topics"].([]map[string]interface{})
	if !ok {
		fmt.Println("No topics to create")
		return nil
	}

	broker := fmt.Sprintf("%s:%d", host.ExternalIP, config.Port)
	brokers := []string{broker}

	for _, topicCfg := range topicsConfig {
		name := topicCfg["name"].(string)                          //nolint:errcheck // config validated by schema
		partitions := int32(topicCfg["partitions"].(int))          //nolint:errcheck // config validated by schema
		replication := int16(topicCfg["replication_factor"].(int)) //nolint:errcheck // config validated by schema

		kafkaConfig := make(map[string]*string)
		if cfg, ok := topicCfg["config"].(map[string]interface{}); ok {
			for k, v := range cfg {
				val := fmt.Sprintf("%v", v)
				kafkaConfig[k] = &val
			}
		}

		created, err := CreateKafkaTopicIfNotExists(brokers, name, partitions, replication, kafkaConfig)
		if err != nil {
			return fmt.Errorf("failed to create topic %s: %w", name, err)
		}

		if created {
			fmt.Printf("✓ Created topic: %s (partitions=%d, replication=%d)\n", name, partitions, replication)
		} else {
			fmt.Printf("Topic %s already exists\n", name)
		}
	}

	return nil
}
