package cmd

import (
	"context"
	"fmt"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterInitCmd creates the init command
func newClusterInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [service]",
		Short: "Initialize databases, topics, and tables",
		Long: `Initialize data structures for infrastructure services:

Available Services:
  postgres    - Create databases and run SQL migrations
  kafka       - Create topics with correct partitions/replication
  clickhouse  - Create databases and tables
  all         - Initialize all services (default)

Initialization is idempotent - safe to run multiple times.
Existing databases/topics/tables will be skipped.`,
		Example: `  frameworks cluster init postgres
  frameworks cluster init kafka
  frameworks cluster init all`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service := "all"
			if len(args) > 0 {
				service = args[0]
			}

			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runInit(cmd, rc, service)
		},
	}

	return cmd
}

// runInit executes the init command against an already-loaded manifest.
func runInit(cmd *cobra.Command, rc *resolvedCluster, service string) error {
	manifest := rc.Manifest
	fmt.Fprintf(cmd.OutOrStdout(), "Initializing %s from manifest: %s\n\n", service, rc.ManifestPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Initialize services based on argument
	switch service {
	case "postgres", "all":
		if err := initPostgres(ctx, cmd, rc, sshPool); err != nil {
			return fmt.Errorf("failed to initialize postgres: %w", err)
		}
	}

	switch service {
	case "kafka", "all":
		if err := initKafka(ctx, cmd, manifest, sshPool); err != nil {
			return fmt.Errorf("failed to initialize kafka: %w", err)
		}
	}

	switch service {
	case "clickhouse", "all":
		if err := initClickHouse(ctx, cmd, manifest, sshPool); err != nil {
			return fmt.Errorf("failed to initialize clickhouse: %w", err)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n✓ Initialization complete!")
	return nil
}

// initPostgres initializes Postgres databases
func initPostgres(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool) error {
	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		fmt.Fprintln(cmd.OutOrStdout(), "Postgres not enabled, skipping...")
		return nil
	}

	// Resolve host — YugabyteDB uses first node, vanilla PG uses Host
	var host inventory.Host
	var ok bool
	if pg.IsYugabyte() && len(pg.Nodes) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Initializing YugabyteDB databases...")
		host, ok = manifest.GetHost(pg.Nodes[0].Host)
		if !ok {
			return fmt.Errorf("yugabyte node host %s not found", pg.Nodes[0].Host)
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Initializing Postgres databases...")
		host, ok = manifest.GetHost(pg.Host)
		if !ok {
			return fmt.Errorf("postgres host %s not found", pg.Host)
		}
	}

	// Build config
	databases := []map[string]string{}
	for _, db := range pg.Databases {
		databases = append(databases, map[string]string{
			"name":  db.Name,
			"owner": db.Owner,
		})
	}

	config := provisioner.ServiceConfig{
		Port: pg.EffectivePort(),
		Metadata: map[string]interface{}{
			"databases": databases,
		},
	}

	// Only decrypt manifest env_files when Yugabyte actually needs a password
	// (i.e. IsYugabyte and no yaml pg.Password). Vanilla Postgres uses peer
	// auth and doesn't need any secret.
	var sharedEnv map[string]string
	if pg.IsYugabyte() && pg.Password == "" {
		env, err := rc.SharedEnv()
		if err != nil {
			return fmt.Errorf("load manifest env_files: %w", err)
		}
		sharedEnv = env
	}
	password, err := resolveYugabytePassword(pg, sharedEnv)
	if err != nil {
		return err
	}
	sqlExec, execErr := newSQLExecutor(pg.SQLAccess, host, pool, pg.IsYugabyte(), password)
	if execErr != nil {
		return fmt.Errorf("create sql executor: %w", execErr)
	}
	opt := provisioner.WithSQLExecutor(sqlExec)

	if pg.IsYugabyte() {
		prov, provErr := provisioner.NewYugabyteProvisioner(pool, opt)
		if provErr != nil {
			return provErr
		}
		return prov.Initialize(ctx, host, config)
	}

	prov, provErr := provisioner.NewPostgresProvisioner(pool, opt)
	if provErr != nil {
		return provErr
	}
	return prov.Initialize(ctx, host, config)
}

// initKafka initializes Kafka topics
func initKafka(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	if manifest.Infrastructure.Kafka == nil || !manifest.Infrastructure.Kafka.Enabled {
		fmt.Fprintln(cmd.OutOrStdout(), "Kafka not enabled, skipping...")
		return nil
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Initializing Kafka topics...")

	// Get first broker host
	if len(manifest.Infrastructure.Kafka.Brokers) == 0 {
		return fmt.Errorf("no kafka brokers configured")
	}

	broker := manifest.Infrastructure.Kafka.Brokers[0]
	host, ok := manifest.GetHost(broker.Host)
	if !ok {
		return fmt.Errorf("kafka broker host %s not found", broker.Host)
	}

	// Create provisioner
	prov, err := provisioner.NewKafkaProvisioner(pool)
	if err != nil {
		return err
	}

	// Build topics config
	topicsConfig := []map[string]interface{}{}
	for _, topic := range manifest.Infrastructure.Kafka.Topics {
		topicCfg := map[string]interface{}{
			"name":               topic.Name,
			"partitions":         topic.Partitions,
			"replication_factor": topic.ReplicationFactor,
		}

		if len(topic.Config) > 0 {
			topicCfg["config"] = topic.Config
		}

		topicsConfig = append(topicsConfig, topicCfg)
	}

	config := provisioner.ServiceConfig{
		Port: broker.Port,
		Metadata: map[string]interface{}{
			"topics": topicsConfig,
		},
	}

	// Initialize
	if err := prov.Initialize(ctx, host, config); err != nil {
		return err
	}

	return nil
}

// initClickHouse initializes ClickHouse databases and tables
func initClickHouse(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	if manifest.Infrastructure.ClickHouse == nil || !manifest.Infrastructure.ClickHouse.Enabled {
		fmt.Fprintln(cmd.OutOrStdout(), "ClickHouse not enabled, skipping...")
		return nil
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Initializing ClickHouse databases and tables...")

	// Get host
	host, ok := manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
	if !ok {
		return fmt.Errorf("clickhouse host %s not found", manifest.Infrastructure.ClickHouse.Host)
	}

	ch := manifest.Infrastructure.ClickHouse
	chExec, chExecErr := newCHExecutor(ch.SQLAccess, host, pool)
	if chExecErr != nil {
		return fmt.Errorf("create ch executor: %w", chExecErr)
	}

	prov, err := provisioner.NewClickHouseProvisioner(pool, provisioner.WithCHExecutor(chExec))
	if err != nil {
		return err
	}

	config := provisioner.ServiceConfig{
		Port: ch.Port,
		Metadata: map[string]any{
			"databases": ch.Databases,
		},
	}

	// Initialize
	if err := prov.Initialize(ctx, host, config); err != nil {
		return err
	}

	return nil
}
