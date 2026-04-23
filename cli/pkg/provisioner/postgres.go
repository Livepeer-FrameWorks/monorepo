package provisioner

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
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
		// sql stays nil in production — sqlExecFor builds an SSHExecutor
		// per-host. Tests inject a mock via WithSQLExecutor.
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

// Validate asserts Postgres is up and every expected database is reachable.
// Uses peer auth through the Unix socket (sudo -u postgres, no -h), which
// matches pg_hba's "local all postgres peer" rule — no password required,
// no TCP. A separate wait_for on the cluster IP verifies the TCP listener
// is also bound where remote peers expect it.
func (p *PostgresProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 5432
	}
	clusterIP := host.ExternalIP
	if clusterIP == "" {
		clusterIP = "127.0.0.1"
	}
	tasks := []ansible.Task{
		waitForTCP("wait for postgres listener", clusterIP, port, 30),
		commandOK("postgres SELECT 1",
			"sudo", "-u", "postgres", "psql", "-p", fmt.Sprintf("%d", port),
			"-d", "postgres", "-tAc", "SELECT 1"),
	}
	for _, db := range databaseNamesFromMetadata(config.Metadata) {
		tasks = append(tasks, commandOK("postgres db="+db+" readiness",
			"sudo", "-u", "postgres", "psql", "-p", fmt.Sprintf("%d", port),
			"-d", db, "-tAc", "SELECT 1"))
	}
	return runValidatePlaybook(ctx, p.executor, p.sshPool.DefaultKeyPath(), host, "postgres", tasks)
}

// sqlExecFor returns the SQLExecutor the provisioner should use against host.
// Tests inject a mock via WithSQLExecutor; production builds an SSHExecutor
// that pipes SQL to psql on the host itself. Postgres uses peer auth over
// the Unix socket (no TCP, no password), so the CLI never needs a direct
// network path to the service port.
func (p *PostgresProvisioner) sqlExecFor(host inventory.Host) (SQLExecutor, error) {
	if p.sql != nil {
		return p.sql, nil
	}
	runner, err := p.GetRunner(host)
	if err != nil {
		return nil, fmt.Errorf("get ssh runner: %w", err)
	}
	return &SSHExecutor{Runner: runner, UsePeerAuth: true}, nil
}

// Initialize creates databases and runs migrations via psql on the host
// over SSH. conn.Host is a placeholder for the executor's log formatting —
// SSHExecutor with UsePeerAuth=true connects via sudo + Unix socket and
// ignores the Host field.
func (p *PostgresProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	exec, err := p.sqlExecFor(host)
	if err != nil {
		return err
	}
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

		created, err := CreateDatabaseIfNotExists(ctx, exec, conn, dbName, owner)
		if err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		if created {
			fmt.Printf("Created database: %s\n", dbName)
		} else {
			fmt.Printf("Database %s already exists\n", dbName)
		}

		dbConn := ConnParams{Host: host.ExternalIP, Port: config.Port, User: "postgres", Database: dbName}
		if err := pgInitializeDatabase(ctx, exec, dbConn, dbName); err != nil {
			return fmt.Errorf("failed to initialize database %s: %w", dbName, err)
		}
	}

	pgPass, _ := config.Metadata["postgres_password"].(string)
	if pgPass != "" {
		ownerDBs := make(map[string][]string)
		for _, db := range dbList {
			owner := db["owner"]
			if owner == "" {
				owner = db["name"]
			}
			ownerDBs[owner] = append(ownerDBs[owner], db["name"])
		}
		for owner, dbs := range ownerDBs {
			if err := pgCreateApplicationUser(ctx, exec, conn, owner, pgPass, dbs); err != nil {
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
	exec, err := p.sqlExecFor(host)
	if err != nil {
		return err
	}
	have := dbSet(manifestDBs)
	for db, path := range staticSeeds {
		if _, ok := have[db]; !ok {
			continue
		}
		conn := ConnParams{Host: host.ExternalIP, Port: port, User: user, Database: db}
		if err := execEmbeddedSQL(ctx, exec, conn, path); err != nil {
			return fmt.Errorf("static seed %s: %w", db, err)
		}
	}
	return nil
}

// ApplyDemoSeeds applies demo data (sample tenant, user, stream) for development.
// Only databases present in manifestDBs are seeded.
func (p *PostgresProvisioner) ApplyDemoSeeds(ctx context.Context, host inventory.Host, port int, user string, manifestDBs []string) error {
	exec, err := p.sqlExecFor(host)
	if err != nil {
		return err
	}
	have := dbSet(manifestDBs)
	for db, path := range demoSeeds {
		if _, ok := have[db]; !ok {
			continue
		}
		conn := ConnParams{Host: host.ExternalIP, Port: port, User: user, Database: db}
		if err := execEmbeddedSQL(ctx, exec, conn, path); err != nil {
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
