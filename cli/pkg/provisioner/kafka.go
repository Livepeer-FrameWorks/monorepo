package provisioner

import (
	"context"
	"fmt"

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

	// Generate Ansible playbook (use address as identifier)
	hostID := host.Address
	if hostID == "" {
		hostID = "localhost"
	}

	playbook := ansible.GenerateKafkaPlaybook(brokerID, hostID, zkConnect)

	// Generate inventory
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    hostID,
		Address: host.Address,
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

// Validate checks if Kafka is healthy
func (k *KafkaProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.KafkaChecker{}

	result := checker.Check(host.Address, config.Port)
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

	broker := fmt.Sprintf("%s:%d", host.Address, config.Port)
	brokers := []string{broker}

	// Create each topic
	for _, topicCfg := range topicsConfig {
		name := topicCfg["name"].(string)
		partitions := int32(topicCfg["partitions"].(int))
		replication := int16(topicCfg["replication_factor"].(int))

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
