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

// PostgresProvisioner provisions PostgreSQL/YugabyteDB
type PostgresProvisioner struct {
	*BaseProvisioner
	executor *ansible.Executor
	sql      SQLExecutor
}

// NewPostgresProvisioner creates a new Postgres provisioner
func NewPostgresProvisioner(pool *ssh.Pool, opts ...ProvisionerOption) (*PostgresProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("failed to create ansible executor: %w", err)
	}

	p := &PostgresProvisioner{
		BaseProvisioner: NewBaseProvisioner("postgres", pool),
		executor:        executor,
		sql:             &DirectExecutor{},
	}
	for _, opt := range opts {
		opt.applyPostgres(p)
	}
	return p, nil
}

// Detect checks if Postgres is installed and running
func (p *PostgresProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "postgres")
}

// Provision installs Postgres via Ansible on every apply. Package-install
// paths (debian, stock-repo rhel) reach changed=0 on rerun via package
// state=present + systemd_service state=started. The source-build path
// (rhel/arch with a pinned version) reaches changed=0 via get_url+unarchive
// cache-hits on a version-keyed sentinel and a creates: gate on
// <prefix>/bin/postgres; a version bump rotates both and rebuilds cleanly.
func (p *PostgresProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	databases := []string{}
	if dbList, ok := config.Metadata["databases"].([]any); ok {
		for _, db := range dbList {
			if dbName, ok := db.(string); ok {
				databases = append(databases, dbName)
			}
		}
	}

	hostID := host.ExternalIP
	if hostID == "" {
		hostID = "localhost"
	}

	family, err := p.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("detect distro family: %w", err)
	}

	version := strings.TrimSpace(config.Version)
	switch version {
	case "latest", "stable":
		version = ""
	}

	params := ansible.PostgresInstallParams{
		DistroFamily: family,
		Version:      version,
		Databases:    databases,
	}

	// Source-build is only used on rhel/arch when a specific version is pinned.
	if version != "" && (family == "rhel" || family == "arch") {
		_, remoteArch, archErr := p.DetectRemoteArch(ctx, host)
		if archErr != nil {
			return fmt.Errorf("detect remote arch: %w", archErr)
		}
		artifact, artifactErr := resolveInfraArtifactFromChannel("postgresql", "linux-"+remoteArch, platformChannelFromMetadata(config.Metadata), config.Metadata)
		if artifactErr != nil {
			return artifactErr
		}
		params.ArtifactURL = artifact.URL
		params.ArtifactChecksum = artifact.Checksum
	}

	playbook := ansible.GeneratePostgresPlaybook(hostID, params)

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    hostID,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": p.sshPool.DefaultKeyPath(),
		},
	})

	execOpts := ansible.ExecuteOptions{
		Verbose: true,
	}

	result, err := p.executor.ExecutePlaybook(ctx, playbook, inv, execOpts)
	if err != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", err, result.Output)
	}

	if !result.Success {
		return fmt.Errorf("ansible playbook failed with %d failures\nOutput: %s",
			result.PlaybookRun.Failures, result.Output)
	}

	return nil
}

// Validate checks Postgres structural state via goss (service running, port
// listening) then the protocol-level health check and database readiness.
// Goss is skipped when an arch probe fails — protocol check still runs.
func (p *PostgresProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if _, remoteArch, err := p.DetectRemoteArch(ctx, host); err == nil {
		// Package-install sets up "postgresql" on debian, "postgresql-*" on rhel;
		// source-build leaves it at "postgresql". Assert on port + the
		// canonical data dir rather than a distro-specific unit name.
		spec := ansible.RenderGossYAML(ansible.GossSpec{
			Ports: map[string]ansible.GossPort{
				fmt.Sprintf("tcp:%d", config.Port): {Listening: true},
			},
		})
		if gossErr := runGossValidate(ctx, p.executor, p.sshPool.DefaultKeyPath(), host,
			"postgres", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
			return fmt.Errorf("postgres goss validate failed: %w", gossErr)
		}
	}

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
	conn := ConnParams{Host: host.ExternalIP, Port: config.Port, User: "postgres", Database: "postgres"}

	dbList, ok := config.Metadata["databases"].([]map[string]string)
	if !ok {
		return fmt.Errorf("databases configuration not found or invalid format")
	}

	var dbNames []string
	for _, db := range dbList {
		dbName := db["name"]
		owner := db["owner"]
		dbNames = append(dbNames, dbName)

		created, err := CreateDatabaseIfNotExists(ctx, p.sql, conn, dbName, owner)
		if err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		if created {
			fmt.Printf("Created database: %s\n", dbName)
		} else {
			fmt.Printf("Database %s already exists\n", dbName)
		}

		dbConn := ConnParams{Host: host.ExternalIP, Port: config.Port, User: "postgres", Database: dbName}
		if err := pgInitializeDatabase(ctx, p.sql, dbConn, dbName); err != nil {
			return fmt.Errorf("failed to initialize database %s: %w", dbName, err)
		}
	}

	pgPass, _ := config.Metadata["postgres_password"].(string)
	if pgPass != "" {
		// Create per-database roles from the owner field in each database entry.
		// Each owner role gets access to its own database only.
		ownerDBs := make(map[string][]string) // owner -> list of db names
		for _, db := range dbList {
			owner := db["owner"]
			if owner == "" {
				owner = db["name"]
			}
			ownerDBs[owner] = append(ownerDBs[owner], db["name"])
		}
		for owner, dbs := range ownerDBs {
			if err := pgCreateApplicationUser(ctx, p.sql, conn, owner, pgPass, dbs); err != nil {
				return fmt.Errorf("failed to create application user %s: %w", owner, err)
			}
		}
	}

	return nil
}

// ApplyStaticSeeds applies production reference data (billing tiers, etc.).
// Only databases present in manifestDBs are seeded; others are skipped so
// partial profiles (e.g. analytics-only without purser) don't fail.
func (p *PostgresProvisioner) ApplyStaticSeeds(ctx context.Context, host inventory.Host, port int, user string, manifestDBs []string) error {
	have := dbSet(manifestDBs)
	for db, path := range staticSeeds {
		if _, ok := have[db]; !ok {
			continue
		}
		conn := ConnParams{Host: host.ExternalIP, Port: port, User: user, Database: db}
		if err := execEmbeddedSQL(ctx, p.sql, conn, path); err != nil {
			return fmt.Errorf("static seed %s: %w", db, err)
		}
	}
	return nil
}

// ApplyDemoSeeds applies demo data (sample tenant, user, stream) for development.
// Only databases present in manifestDBs are seeded.
func (p *PostgresProvisioner) ApplyDemoSeeds(ctx context.Context, host inventory.Host, port int, user string, manifestDBs []string) error {
	have := dbSet(manifestDBs)
	for db, path := range demoSeeds {
		if _, ok := have[db]; !ok {
			continue
		}
		conn := ConnParams{Host: host.ExternalIP, Port: port, User: user, Database: db}
		if err := execEmbeddedSQL(ctx, p.sql, conn, path); err != nil {
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

func databaseNamesFromMetadata(metadata map[string]any) []string {
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
	case []map[string]any:
		for _, entry := range v {
			if name, ok := entry["name"].(string); ok {
				addName(name)
			}
		}
	case []any:
		for _, entry := range v {
			switch typed := entry.(type) {
			case string:
				addName(typed)
			case map[string]string:
				addName(typed["name"])
			case map[string]any:
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
