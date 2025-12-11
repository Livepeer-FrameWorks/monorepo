package health

import (
	"fmt"
	"time"

	"github.com/IBM/sarama"
)

// KafkaChecker checks Kafka broker health
type KafkaChecker struct {
	// Config for connection
}

// Check performs a health check on a Kafka broker
func (c *KafkaChecker) Check(address string, port int) *CheckResult {
	result := &CheckResult{
		Name:      "kafka",
		CheckedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	start := time.Now()
	brokers := []string{fmt.Sprintf("%s:%d", address, port)}

	config := sarama.NewConfig()
	config.Version = sarama.V3_0_0_0 // Compatible with Kafka 3.9.1
	config.Net.DialTimeout = 5 * time.Second
	config.Net.ReadTimeout = 5 * time.Second
	config.Net.WriteTimeout = 5 * time.Second

	// Try to connect
	client, err := sarama.NewClient(brokers, config)
	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("failed to connect: %v", err)
		return result
	}
	defer client.Close()

	result.Latency = time.Since(start)

	// Get broker info
	broker := client.Brokers()[0]
	if broker != nil {
		result.Metadata["broker_id"] = fmt.Sprintf("%d", broker.ID())
		result.Metadata["broker_addr"] = broker.Addr()
	}

	// List topics (validates connectivity)
	topics, err := client.Topics()
	if err != nil {
		result.OK = false
		result.Status = "degraded"
		result.Error = fmt.Sprintf("failed to list topics: %v", err)
		return result
	}

	result.Metadata["topics"] = fmt.Sprintf("%d", len(topics))

	// Check if we can get controller
	controller, err := client.Controller()
	if err != nil {
		result.OK = false
		result.Status = "degraded"
		result.Message = "Connected but no controller available"
		result.Metadata["controller_error"] = err.Error()
		return result
	}

	result.Metadata["controller_id"] = fmt.Sprintf("%d", controller.ID())

	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("Connected successfully (latency: %v, %d topics)", result.Latency, len(topics))

	return result
}
