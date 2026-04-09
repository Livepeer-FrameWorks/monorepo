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

// KafkaProvisioner provisions Kafka brokers
type KafkaProvisioner struct {
	*BaseProvisioner
	executor *ansible.Executor
}

// NewKafkaProvisioner creates a new Kafka provisioner
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

// Detect checks if Kafka is installed and running
func (k *KafkaProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return k.CheckExists(ctx, host, "kafka")
}

// Provision installs Kafka using Ansible
func (k *KafkaProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check if already installed
	state, err := k.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil // Already provisioned
	}

	// Get broker ID and Zookeeper connection from config
	brokerID, ok := config.Metadata["broker_id"].(int)
	if !ok {
		return fmt.Errorf("broker_id not found in config")
	}

	zkConnect, ok := config.Metadata["zookeeper_connect"].(string)
	if !ok {
		return fmt.Errorf("zookeeper_connect not found in config")
	}
	err = validateApacheKafkaVersion(config.Version)
	if err != nil {
		return err
	}

	// Generate Ansible playbook (use address as identifier)
	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	port := config.Port
	if port == 0 {
		port = 9092
	}

	playbook := ansible.GenerateKafkaPlaybook(config.Version, brokerID, hostID, port, zkConnect, config.Metadata)

	// Generate inventory
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    hostID,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": host.SSHKey,
		},
	})

	// Execute playbook
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
		return fmt.Errorf("kafka native mode expects an Apache Kafka version such as 3.6.0; got %q", version)
	}
	return nil
}

// Validate checks if Kafka is healthy
func (k *KafkaProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.KafkaChecker{}

	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("kafka health check failed: %s", result.Error)
	}

	return nil
}

// Initialize creates Kafka topics
func (k *KafkaProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Get topics configuration
	topicsConfig, ok := config.Metadata["topics"].([]map[string]interface{})
	if !ok {
		fmt.Println("No topics to create")
		return nil
	}

	broker := fmt.Sprintf("%s:%d", host.ExternalIP, config.Port)
	brokers := []string{broker}

	// Create each topic
	for _, topicCfg := range topicsConfig {
		name := topicCfg["name"].(string)                          //nolint:errcheck // config validated by schema
		partitions := int32(topicCfg["partitions"].(int))          //nolint:errcheck // config validated by schema
		replication := int16(topicCfg["replication_factor"].(int)) //nolint:errcheck // config validated by schema

		// Convert config to Kafka format
		kafkaConfig := make(map[string]*string)
		if cfg, ok := topicCfg["config"].(map[string]interface{}); ok {
			for k, v := range cfg {
				val := fmt.Sprintf("%v", v)
				kafkaConfig[k] = &val
			}
		}

		// Create topic if not exists
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
