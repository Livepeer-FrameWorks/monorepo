package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/IBM/sarama"
	"github.com/lib/pq" // Postgres driver
)

func buildCreateDatabaseQuery(dbName, owner string) string {
	query := fmt.Sprintf("CREATE DATABASE %s", pq.QuoteIdentifier(dbName))
	if owner != "" {
		query = fmt.Sprintf("CREATE DATABASE %s OWNER %s", pq.QuoteIdentifier(dbName), pq.QuoteIdentifier(owner))
	}
	return query
}

// DatabaseExists checks if a Postgres database exists
func DatabaseExists(ctx context.Context, connStr, dbName string) (bool, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return false, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer db.Close()

	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)"
	err = db.QueryRowContext(ctx, query, dbName).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check database existence: %w", err)
	}

	return exists, nil
}

// CreateDatabaseIfNotExists creates a Postgres database if it doesn't exist
func CreateDatabaseIfNotExists(ctx context.Context, connStr, dbName, owner string) (bool, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return false, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer db.Close()

	// Check if database exists
	exists, err := DatabaseExists(ctx, connStr, dbName)
	if err != nil {
		return false, err
	}

	if exists {
		return false, nil // Already exists, no changes made
	}

	// Create database
	query := buildCreateDatabaseQuery(dbName, owner)

	if _, err := db.ExecContext(ctx, query); err != nil {
		return false, fmt.Errorf("failed to create database: %w", err)
	}

	return true, nil // Database created
}

// TableExists checks if a table exists in a Postgres database
func TableExists(ctx context.Context, connStr, tableName string) (bool, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return false, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer db.Close()

	// Parse schema.table if provided
	parts := strings.Split(tableName, ".")
	var schema, table string

	if len(parts) == 2 {
		schema = parts[0]
		table = parts[1]
	} else {
		schema = "public"
		table = tableName
	}

	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)"
	err = db.QueryRowContext(ctx, query, schema, table).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check table existence: %w", err)
	}

	return exists, nil
}

// ExecuteSQLFile executes a SQL file (idempotent - safe to run multiple times)
func ExecuteSQLFile(ctx context.Context, connStr, sqlContent string) error {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer db.Close()

	// Execute SQL (may contain multiple statements)
	if _, err := db.ExecContext(ctx, sqlContent); err != nil {
		return fmt.Errorf("failed to execute SQL: %w", err)
	}

	return nil
}

// KafkaTopicExists checks if a Kafka topic exists
func KafkaTopicExists(brokers []string, topic string) (bool, error) {
	config := sarama.NewConfig()
	config.Version = sarama.V2_6_0_0

	admin, err := sarama.NewClusterAdmin(brokers, config)
	if err != nil {
		return false, fmt.Errorf("failed to create Kafka admin client: %w", err)
	}
	defer admin.Close()

	topics, err := admin.ListTopics()
	if err != nil {
		return false, fmt.Errorf("failed to list topics: %w", err)
	}

	_, exists := topics[topic]
	return exists, nil
}

// CreateKafkaTopicIfNotExists creates a Kafka topic if it doesn't exist
func CreateKafkaTopicIfNotExists(brokers []string, topic string, partitions int32, replication int16, config map[string]*string) (bool, error) {
	// Check if topic exists
	exists, err := KafkaTopicExists(brokers, topic)
	if err != nil {
		return false, err
	}

	if exists {
		return false, nil // Already exists
	}

	// Create admin client
	adminConfig := sarama.NewConfig()
	adminConfig.Version = sarama.V2_6_0_0

	admin, err := sarama.NewClusterAdmin(brokers, adminConfig)
	if err != nil {
		return false, fmt.Errorf("failed to create Kafka admin client: %w", err)
	}
	defer admin.Close()

	// Create topic
	topicDetail := &sarama.TopicDetail{
		NumPartitions:     partitions,
		ReplicationFactor: replication,
		ConfigEntries:     config,
	}

	if err := admin.CreateTopic(topic, topicDetail, false); err != nil {
		return false, fmt.Errorf("failed to create topic: %w", err)
	}

	return true, nil // Topic created
}

// FileExists checks if a file exists on a remote host via command
func FileExistsCommand(remotePath string) string {
	return fmt.Sprintf("test -f %s && echo 'exists' || echo 'notfound'", remotePath)
}

// DirectoryExistsCommand checks if a directory exists on a remote host via command
func DirectoryExistsCommand(remotePath string) string {
	return fmt.Sprintf("test -d %s && echo 'exists' || echo 'notfound'", remotePath)
}

// ServiceRunningCommand checks if a systemd service is running
func ServiceRunningCommand(serviceName string) string {
	return fmt.Sprintf("systemctl is-active %s && echo 'running' || echo 'stopped'", serviceName)
}
