package cmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"frameworks/cli/internal/ux"
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
  postgres    - Create databases/schemas for PostgreSQL
  yugabyte    - Create databases/schemas for YugabyteDB
  kafka       - Create topics with correct partitions/replication
  clickhouse  - Create databases and tables
  all         - Initialize all services (default)

Initialization is idempotent - safe to run multiple times.
Existing databases/topics/tables will be skipped.`,
		Example: `  frameworks cluster init yugabyte
  frameworks cluster init postgres
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
	out := cmd.OutOrStdout()
	ux.Heading(out, fmt.Sprintf("Initializing %s from manifest: %s", service, rc.ManifestPath))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	switch service {
	case "postgres", "yugabyte", "database", "all":
		if err := initPostgres(ctx, cmd, rc, sshPool); err != nil {
			return fmt.Errorf("failed to initialize database backend: %w", err)
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

	ux.Success(out, "Initialization complete")
	ux.PrintNextSteps(out, []ux.NextStep{
		{Cmd: "frameworks cluster seed", Why: "Load static seed data (billing tiers, reference data)."},
		{Cmd: "frameworks cluster doctor", Why: "Verify the cluster is ready."},
	})
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
		Version: pg.Version,
		Port:    pg.EffectivePort(),
		Metadata: map[string]interface{}{
			"platform_channel": manifest.ResolvedChannel(),
			"databases":        databases,
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
	// The role's init tag uses community.postgresql over the network;
	// password is propagated as a role var via the registry-provided
	// provisioner, which reads postgres_password from metadata.
	config.Metadata["postgres_password"] = password

	service := "postgres"
	if pg.IsYugabyte() {
		service = "yugabyte"
	}
	prov, provErr := provisioner.GetProvisioner(service, pool)
	if provErr != nil {
		return provErr
	}
	if err := prov.Initialize(ctx, host, config); err != nil {
		return err
	}

	schemaDatabases := make([]provisioner.SchemaDatabase, 0, len(pg.Databases))
	for _, db := range pg.Databases {
		schemaDatabases = append(schemaDatabases, provisioner.SchemaDatabase{
			Name:  db.Name,
			Owner: db.Owner,
		})
	}
	return applyPostgresSchemasAndMigrations(ctx, cmd.OutOrStdout(), service, host, config, prov, schemaDatabases)
}

func applyPostgresSchemasAndMigrations(
	ctx context.Context,
	out io.Writer,
	service string,
	host inventory.Host,
	config provisioner.ServiceConfig,
	prov provisioner.Provisioner,
	databases []provisioner.SchemaDatabase,
) error {
	schemaItems, schemaCleanup, err := provisioner.BuildSchemaItems(databases)
	defer schemaCleanup()
	if err != nil {
		return fmt.Errorf("collect baseline schemas: %w", err)
	}
	if len(schemaItems) > 0 {
		applier, ok := prov.(provisioner.SchemaApplier)
		if !ok {
			return fmt.Errorf("%s provisioner does not implement SchemaApplier", service)
		}
		cfg := configWithMetadata(config)
		schemaKey := "postgres_schema_items"
		if service == "yugabyte" {
			schemaKey = "yugabyte_schema_items"
		}
		cfg.Metadata[schemaKey] = schemaItems
		fmt.Fprintf(out, "Applying %s baseline schemas...\n", service)
		if applyErr := applier.ApplySchemas(ctx, host, cfg); applyErr != nil {
			return fmt.Errorf("apply %s baseline schemas: %w", service, applyErr)
		}
		ux.Success(out, fmt.Sprintf("%s baseline schemas applied", service))
	}

	dbNames := make([]string, 0, len(databases))
	for _, db := range databases {
		dbNames = append(dbNames, db.Name)
	}
	migrationItems, err := provisioner.BuildMigrationItems(dbNames)
	if err != nil {
		return fmt.Errorf("collect migrations: %w", err)
	}
	if len(migrationItems) == 0 {
		return nil
	}
	migrator, ok := prov.(provisioner.Migrator)
	if !ok {
		return fmt.Errorf("%s provisioner does not implement Migrator", service)
	}
	cfg := configWithMetadata(config)
	migrateKey := "postgres_migrate_items"
	if service == "yugabyte" {
		migrateKey = "yugabyte_migrate_items"
	}
	cfg.Metadata[migrateKey] = migrationItems
	fmt.Fprintf(out, "Applying %s migrations...\n", service)
	if err := migrator.ApplyMigrations(ctx, host, cfg, false); err != nil {
		return fmt.Errorf("apply %s migrations: %w", service, err)
	}
	ux.Success(out, fmt.Sprintf("%s migrations applied", service))
	return nil
}

func configWithMetadata(config provisioner.ServiceConfig) provisioner.ServiceConfig {
	metadata := make(map[string]any, len(config.Metadata)+1)
	for k, v := range config.Metadata {
		metadata[k] = v
	}
	config.Metadata = metadata
	return config
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

	prov, err := provisioner.GetProvisioner("kafka", pool)
	if err != nil {
		return err
	}

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

	return prov.Initialize(ctx, host, config)
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
	prov, err := provisioner.GetProvisioner("clickhouse", pool)
	if err != nil {
		return err
	}

	config := provisioner.ServiceConfig{
		Port: ch.Port,
		Metadata: map[string]any{
			"databases": ch.Databases,
		},
	}

	return prov.Initialize(ctx, host, config)
}
