package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	dbsql "frameworks/pkg/database/sql"

	"github.com/lib/pq"
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
	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	playbook := ansible.GeneratePostgresPlaybook(hostID, config.Version, databases)

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

	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("postgres health check failed: %s", result.Error)
	}

	dbNames := databaseNamesFromMetadata(config.Metadata)
	if len(dbNames) > 0 {
		dbResult := checker.CheckDatabases(host.ExternalIP, config.Port, dbNames)
		if !dbResult.OK {
			return fmt.Errorf("postgres database readiness failed: %s", dbResult.Error)
		}
	}

	return nil
}

// Initialize creates databases and runs migrations
func (p *PostgresProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Connection string for postgres database
	connStr := fmt.Sprintf("host=%s port=%d user=postgres dbname=postgres sslmode=disable",
		host.ExternalIP, config.Port)

	// Get databases to initialize from config
	dbList, ok := config.Metadata["databases"].([]map[string]string)
	if !ok {
		return fmt.Errorf("databases configuration not found or invalid format")
	}

	// Create each database if it doesn't exist
	var dbNames []string
	for _, db := range dbList {
		dbName := db["name"]
		owner := db["owner"]
		dbNames = append(dbNames, dbName)

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

	// Create application user if credentials are provided.
	pgUser, _ := config.Metadata["postgres_user"].(string)
	pgPass, _ := config.Metadata["postgres_password"].(string)
	if pgUser != "" && pgPass != "" {
		if err := p.createApplicationUser(ctx, host, config.Port, pgUser, pgPass, dbNames); err != nil {
			return fmt.Errorf("failed to create application user: %w", err)
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
		"skipper":       "schema/skipper.sql",
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

// createApplicationUser creates a Postgres role for application services.
// Idempotent: creates the role if missing, updates the password on re-provision.
func (p *PostgresProvisioner) createApplicationUser(ctx context.Context, host inventory.Host, port int, user, password string, databases []string) error {
	connStr := fmt.Sprintf("host=%s port=%d user=postgres dbname=postgres sslmode=disable",
		host.ExternalIP, port)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("connect as superuser: %w", err)
	}
	defer db.Close()

	// CREATE ROLE IF NOT EXISTS — Postgres lacks this syntax before v14,
	// so check pg_roles first.
	var exists bool
	err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", user).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check role existence: %w", err)
	}

	quotedUser := pq.QuoteIdentifier(user)
	if !exists {
		// Use parameterised password via ALTER below; CREATE ROLE doesn't support $1 for PASSWORD.
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE ROLE %s WITH LOGIN", quotedUser)); err != nil {
			return fmt.Errorf("create role %s: %w", user, err)
		}
		fmt.Printf("Created Postgres role: %s\n", user)
	}

	// Always set/update the password (handles rotation on re-provision).
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s", quotedUser, pq.QuoteLiteral(password))); err != nil {
		return fmt.Errorf("set password for %s: %w", user, err)
	}

	// Grant privileges on each database.
	for _, dbName := range databases {
		quotedDB := pq.QuoteIdentifier(dbName)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %s TO %s", quotedDB, quotedUser)); err != nil {
			return fmt.Errorf("grant on %s: %w", dbName, err)
		}

		// Grant schema-level privileges so the role can use existing tables.
		dbConn, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d user=postgres dbname=%s sslmode=disable", host.ExternalIP, port, dbName))
		if err != nil {
			return fmt.Errorf("connect to %s: %w", dbName, err)
		}
		schemaGrants := []string{
			fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", quotedUser),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO %s", quotedUser),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO %s", quotedUser),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO %s", quotedUser),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO %s", quotedUser),
		}
		for _, stmt := range schemaGrants {
			if _, err := dbConn.ExecContext(ctx, stmt); err != nil {
				dbConn.Close()
				return fmt.Errorf("schema grant on %s: %w", dbName, err)
			}
		}
		dbConn.Close()
	}

	fmt.Printf("✓ Postgres role %s ready (grants on %d databases)\n", user, len(databases))
	return nil
}

func (p *PostgresProvisioner) executeEmbeddedSQL(ctx context.Context, host inventory.Host, port int, dbName, relativePath string) error {
	return p.executeEmbeddedSQLAs(ctx, host, port, "postgres", dbName, relativePath)
}

func (p *PostgresProvisioner) executeEmbeddedSQLAs(ctx context.Context, host inventory.Host, port int, user, dbName, relativePath string) error {
	sqlContent, err := dbsql.Content.ReadFile(relativePath)
	if err != nil {
		return fmt.Errorf("failed to read embedded SQL file %s: %w", relativePath, err)
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable",
		host.ExternalIP, port, user, dbName)

	return ExecuteSQLFile(ctx, connStr, string(sqlContent))
}

// ApplyStaticSeeds applies production reference data (billing tiers, etc.).
// Only databases present in manifestDBs are seeded; others are skipped so
// partial profiles (e.g. analytics-only without purser) don't fail.
func (p *PostgresProvisioner) ApplyStaticSeeds(ctx context.Context, host inventory.Host, port int, user string, manifestDBs []string) error {
	seeds := map[string]string{
		"purser": "seeds/static/purser_tiers.sql",
	}
	have := dbSet(manifestDBs)
	for db, path := range seeds {
		if _, ok := have[db]; !ok {
			continue
		}
		if err := p.executeEmbeddedSQLAs(ctx, host, port, user, db, path); err != nil {
			return fmt.Errorf("static seed %s: %w", db, err)
		}
	}
	return nil
}

// ApplyDemoSeeds applies demo data (sample tenant, user, stream) for development.
// Only databases present in manifestDBs are seeded.
func (p *PostgresProvisioner) ApplyDemoSeeds(ctx context.Context, host inventory.Host, port int, user string, manifestDBs []string) error {
	demos := map[string]string{
		"quartermaster": "seeds/demo/demo_data.sql",
	}
	have := dbSet(manifestDBs)
	for db, path := range demos {
		if _, ok := have[db]; !ok {
			continue
		}
		if err := p.executeEmbeddedSQLAs(ctx, host, port, user, db, path); err != nil {
			return fmt.Errorf("demo seed %s: %w", db, err)
		}
	}
	return nil
}

func dbSet(names []string) map[string]struct{} {
	s := make(map[string]struct{}, len(names))
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}

func databaseNamesFromMetadata(metadata map[string]interface{}) []string {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["databases"]
	if !ok || raw == nil {
		return nil
	}

	names := map[string]struct{}{}
	addName := func(name string) {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			names[trimmed] = struct{}{}
		}
	}

	switch v := raw.(type) {
	case []string:
		for _, name := range v {
			addName(name)
		}
	case []map[string]string:
		for _, entry := range v {
			addName(entry["name"])
		}
	case []map[string]interface{}:
		for _, entry := range v {
			if name, ok := entry["name"].(string); ok {
				addName(name)
			}
		}
	case []interface{}:
		for _, entry := range v {
			switch typed := entry.(type) {
			case string:
				addName(typed)
			case map[string]string:
				addName(typed["name"])
			case map[string]interface{}:
				if name, ok := typed["name"].(string); ok {
					addName(name)
				}
			}
		}
	}

	if len(names) == 0 {
		return nil
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	return out
}
