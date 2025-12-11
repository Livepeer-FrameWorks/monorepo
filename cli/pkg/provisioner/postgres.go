package provisioner

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	dbsql "frameworks/pkg/database/sql"
)

// PostgresProvisioner provisions PostgreSQL/YugabyteDB
type PostgresProvisioner struct {
	*BaseProvisioner
	executor *ansible.Executor
}

// NewPostgresProvisioner creates a new Postgres provisioner
func NewPostgresProvisioner(pool *ssh.Pool) (*PostgresProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("failed to create ansible executor: %w", err)
	}

	return &PostgresProvisioner{
		BaseProvisioner: NewBaseProvisioner("postgres", pool),
		executor:        executor,
	}, nil
}

// Detect checks if Postgres is installed and running
func (p *PostgresProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "postgres")
}

// Provision installs Postgres using Ansible
func (p *PostgresProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check if already installed
	state, err := p.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil // Already provisioned
	}

	// Get database list from config
	databases := []string{}
	if dbList, ok := config.Metadata["databases"].([]interface{}); ok {
		for _, db := range dbList {
			if dbName, ok := db.(string); ok {
				databases = append(databases, dbName)
			}
		}
	}

	// Generate Ansible playbook (use address as identifier)
	hostID := host.Address
	if hostID == "" {
		hostID = "localhost"
	}

	playbook := ansible.GeneratePostgresPlaybook(hostID, config.Version, databases)

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

	result, err := p.executor.ExecutePlaybook(ctx, playbook, inv, opts)
	if err != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", err, result.Output)
	}

	if !result.Success {
		return fmt.Errorf("ansible playbook failed with %d failures\nOutput: %s",
			result.PlaybookRun.Failures, result.Output)
	}

	return nil
}

// Validate checks if Postgres is healthy
func (p *PostgresProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.PostgresChecker{
		User:     "postgres",
		Password: "",
		Database: "postgres",
	}

	result := checker.Check(host.Address, config.Port)
	if !result.OK {
		return fmt.Errorf("postgres health check failed: %s", result.Error)
	}

	return nil
}

// Initialize creates databases and runs migrations
func (p *PostgresProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Connection string for postgres database
	connStr := fmt.Sprintf("host=%s port=%d user=postgres dbname=postgres sslmode=disable",
		host.Address, config.Port)

	// Get databases to initialize from config
	dbList, ok := config.Metadata["databases"].([]map[string]string)
	if !ok {
		return fmt.Errorf("databases configuration not found or invalid format")
	}

	// Create each database if it doesn't exist
	for _, db := range dbList {
		dbName := db["name"]
		owner := db["owner"]

		// Create database
		created, err := CreateDatabaseIfNotExists(ctx, connStr, dbName, owner)
		if err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		if created {
			fmt.Printf("Created database: %s\n", dbName)
		} else {
			fmt.Printf("Database %s already exists\n", dbName)
		}

		// Run initialization SQL for this database
		if err := p.initializeDatabase(ctx, host, config.Port, dbName); err != nil {
			return fmt.Errorf("failed to initialize database %s: %w", dbName, err)
		}
	}

	return nil
}

// initializeDatabase runs SQL migrations for a specific database
func (p *PostgresProvisioner) initializeDatabase(ctx context.Context, host inventory.Host, port int, dbName string) error {
	// 1. Apply Schema
	schemaFiles := map[string]string{
		"quartermaster": "schema/quartermaster.sql",
		"commodore":     "schema/commodore.sql",
		"foghorn":       "schema/foghorn.sql",
		"periscope":     "schema/periscope.sql",
		"purser":        "schema/purser.sql",
		"listmonk":      "schema/listmonk.sql",
		"navigator":     "schema/navigator.sql",
	}

	if schemaFile, ok := schemaFiles[dbName]; ok {
		fmt.Printf("Applying schema for %s...\n", dbName)
		if err := p.executeEmbeddedSQL(ctx, host, port, dbName, schemaFile); err != nil {
			return fmt.Errorf("failed to apply schema for %s: %w", dbName, err)
		}
	} else {
		fmt.Printf("No dedicated schema file found for %s, skipping schema step.\n", dbName)
	}

	// 2. Apply Static Seeds (Production Data)
	staticSeeds := map[string]string{
		"purser": "seeds/static/purser_tiers.sql",
	}

	if seedFile, ok := staticSeeds[dbName]; ok {
		fmt.Printf("Applying static seeds for %s...\n", dbName)
		if err := p.executeEmbeddedSQL(ctx, host, port, dbName, seedFile); err != nil {
			return fmt.Errorf("failed to apply static seeds for %s: %w", dbName, err)
		}
	}

	// 3. Apply Demo Seeds (Skipped by default in this flow)

	fmt.Printf("✓ Database %s initialized\n", dbName)
	return nil
}

func (p *PostgresProvisioner) executeEmbeddedSQL(ctx context.Context, host inventory.Host, port int, dbName, relativePath string) error {
	sqlContent, err := dbsql.Content.ReadFile(relativePath)
	if err != nil {
		return fmt.Errorf("failed to read embedded SQL file %s: %w", relativePath, err)
	}

	connStr := fmt.Sprintf("host=%s port=%d user=postgres dbname=%s sslmode=disable",
		host.Address, port, dbName)

	return ExecuteSQLFile(ctx, connStr, string(sqlContent))
}
