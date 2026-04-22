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

// ClickHouseProvisioner provisions ClickHouse
type ClickHouseProvisioner struct {
	*BaseProvisioner
	ch       CHExecutor
	executor *ansible.Executor
}

// NewClickHouseProvisioner creates a new ClickHouse provisioner
func NewClickHouseProvisioner(pool *ssh.Pool, opts ...ProvisionerOption) (*ClickHouseProvisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("create ansible executor: %w", err)
	}
	p := &ClickHouseProvisioner{
		BaseProvisioner: NewBaseProvisioner("clickhouse", pool),
		ch:              &DirectCHExecutor{},
		executor:        executor,
	}
	for _, opt := range opts {
		opt.applyClickHouse(p)
	}
	return p, nil
}

// Detect checks if ClickHouse is installed and running
func (c *ClickHouseProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return c.CheckExists(ctx, host, "clickhouse")
}

// Provision installs ClickHouse using declarative Ansible tasks. Every distro
// follows the same pinned-artifact path: fetch the vendor tarball whose URL +
// checksum come from the release manifest, extract it, and let the bundled
// `clickhouse install` helper bootstrap /etc, /var/lib, users, and the
// systemd unit. Runs every apply — same-version reruns are changed=0 via
// get_url checksum match + version-keyed unarchive sentinel + install
// sentinel; version bumps rotate both sentinels so the host converges to
// the pinned version.
func (c *ClickHouseProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	family, err := c.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("detect distro family: %w", err)
	}
	switch family {
	case "debian", "rhel", "arch":
	default:
		return fmt.Errorf("clickhouse: unsupported distro family %q", family)
	}

	_, remoteArch, archErr := c.DetectRemoteArch(ctx, host)
	if archErr != nil {
		return fmt.Errorf("detect remote arch: %w", archErr)
	}
	artifact, artifactErr := resolveInfraArtifactFromChannel("clickhouse", "linux-"+remoteArch, platformChannelFromMetadata(config.Metadata), config.Metadata)
	if artifactErr != nil {
		return artifactErr
	}

	tasks := clickhouseInstallTasks(artifact.URL, artifact.Checksum)
	tasks = append(tasks, ansible.TaskSystemdService("clickhouse-server", ansible.SystemdOpts{
		State:        "started",
		Enabled:      ansible.BoolPtr(true),
		DaemonReload: true,
	}))

	playbook := &ansible.Playbook{
		Name:  "Provision ClickHouse",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Provision ClickHouse",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
				Tasks:       tasks,
			},
		},
	}

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": c.sshPool.DefaultKeyPath(),
		},
	})

	result, execErr := c.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("ansible playbook failed\nOutput: %s", result.Output)
	}

	return nil
}

// clickhouseInstallTasks installs ClickHouse from the pinned vendor tarball
// (release-manifest-pinned). Tarball wraps a single
// `clickhouse-common-static-<version>-<arch>/` top dir; --strip-components=1
// drops it so the inner usr/bin/clickhouse lands at
// /tmp/clickhouse-install/usr/bin/clickhouse. Works identically on Debian,
// RHEL, and Arch — `clickhouse install` bootstraps everything distro-specific.
func clickhouseInstallTasks(artifactURL, artifactChecksum string) []ansible.Task {
	// Sentinels rotate with the pinned artifact identity so version bumps
	// trigger re-extract + re-install instead of silently skipping.
	extractSentinel := ansible.ArtifactSentinel("/tmp/clickhouse-install", artifactChecksum+artifactURL)
	installSentinel := ansible.ArtifactSentinel("/var/lib/clickhouse", artifactChecksum+artifactURL)
	return []ansible.Task{
		ansible.TaskPackage("curl", ansible.PackagePresent),
		ansible.TaskPackage("tar", ansible.PackagePresent),
		{
			Name:   "create /tmp/clickhouse-install",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/tmp/clickhouse-install", "state": "directory", "mode": "0755"},
		},
		// Tarball + extract dir are both intentionally left in /tmp so
		// subsequent same-version applies short-circuit: get_url skips via
		// checksum match, unarchive skips via creates: gate.
		ansible.TaskGetURL(artifactURL, "/tmp/clickhouse.tgz", artifactChecksum),
		ansible.TaskUnarchive("/tmp/clickhouse.tgz", "/tmp/clickhouse-install", extractSentinel,
			ansible.UnarchiveOpts{StripComponents: 1}),
		ansible.TaskShell("touch "+extractSentinel, ansible.ShellOpts{Creates: extractSentinel}),
		// Vendor's `clickhouse install` bootstraps /etc, /var/lib, users, and
		// the systemd unit. Version-keyed sentinel so a pin bump re-runs it.
		ansible.TaskShell(
			"/tmp/clickhouse-install/usr/bin/clickhouse install --noninteractive --user clickhouse --group clickhouse && "+
				"install -d -o clickhouse -g clickhouse /var/lib/clickhouse && "+
				"touch "+installSentinel+" && chown clickhouse:clickhouse "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel},
		),
	}
}

// Validate checks ClickHouse structural state via goss, then the HTTP
// health endpoint.
func (c *ClickHouseProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if _, remoteArch, err := c.DetectRemoteArch(ctx, host); err == nil {
		spec := ansible.RenderGossYAML(ansible.GossSpec{
			Services: map[string]ansible.GossService{
				"clickhouse-server": {Running: true, Enabled: true},
			},
			Ports: map[string]ansible.GossPort{
				fmt.Sprintf("tcp:%d", config.Port): {Listening: true},
			},
			Files: map[string]ansible.GossFile{
				"/usr/bin/clickhouse-server": {Exists: true},
			},
		})
		if gossErr := runGossValidate(ctx, c.executor, c.sshPool.DefaultKeyPath(), host,
			"clickhouse", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
			return fmt.Errorf("clickhouse goss validate failed: %w", gossErr)
		}
	}

	checker := &health.ClickHouseChecker{
		User:     "default",
		Password: "",
		Database: "default",
	}

	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("clickhouse health check failed: %s", result.Error)
	}

	return nil
}

// Initialize creates databases and tables.
// Connects as "default" with no password because this runs before Configure
// sets up authenticated users. Callers must ensure Initialize runs before
// Configure to avoid an unauthenticated-access window after auth is applied.
func (c *ClickHouseProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	databases, ok := config.Metadata["databases"].([]string)
	if !ok {
		databases = []string{"periscope"}
	}

	for _, dbName := range databases {
		query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", dbName)
		if err := c.ch.Exec(ctx, host.ExternalIP, config.Port, "default", "", "default", query); err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		fmt.Printf("✓ Database %s ready\n", dbName)

		if dbName == "periscope" {
			sqlContent, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
			if err != nil {
				return fmt.Errorf("failed to read embedded ClickHouse schema: %w", err)
			}
			if err := c.ch.Exec(ctx, host.ExternalIP, config.Port, "default", "", "periscope", string(sqlContent)); err != nil {
				return fmt.Errorf("failed to initialize periscope database: %w", err)
			}
			fmt.Println("✓ ClickHouse periscope schema initialized")
		}
	}

	return nil
}

// Configure deploys users.xml and sets the CLICKHOUSE_PASSWORD env var
// on the ClickHouse host so the "frameworks" user has a password.
// Must be called after Provision (ClickHouse is installed and running).
// BuildClickHouseUsersXML returns the /etc/clickhouse-server/users.xml bytes.
func BuildClickHouseUsersXML() []byte {
	return []byte(`<?xml version="1.0"?>
<clickhouse>
    <profiles>
        <default>
            <log_queries>1</log_queries>
            <log_query_threads>1</log_query_threads>
            <allow_ddl>1</allow_ddl>
            <readonly>0</readonly>
        </default>
        <readonly>
            <log_queries>0</log_queries>
            <readonly>1</readonly>
            <allow_ddl>0</allow_ddl>
        </readonly>
    </profiles>
    <users>
        <default>
            <password></password>
            <profile>default</profile>
            <quota>default</quota>
            <networks>
                <ip>::1</ip>
                <ip>127.0.0.1</ip>
            </networks>
            <access_management>1</access_management>
        </default>
        <frameworks>
            <password from_env="CLICKHOUSE_PASSWORD"></password>
            <profile>default</profile>
            <quota>frameworks_quota</quota>
            <networks>
                <ip>::/0</ip>
            </networks>
            <access_management>0</access_management>
            <allow_databases>
                <database>periscope</database>
            </allow_databases>
        </frameworks>
        <readonly_user>
            <password from_env="CLICKHOUSE_READONLY_PASSWORD"></password>
            <profile>readonly</profile>
            <quota>readonly_quota</quota>
            <networks>
                <ip>::/0</ip>
            </networks>
            <allow_databases>
                <database>periscope</database>
            </allow_databases>
        </readonly_user>
    </users>
    <quotas>
        <default>
            <interval>
                <duration>3600</duration>
                <queries>0</queries>
                <errors>0</errors>
                <result_rows>0</result_rows>
                <read_rows>0</read_rows>
                <execution_time>0</execution_time>
            </interval>
        </default>
        <frameworks_quota>
            <interval>
                <duration>3600</duration>
                <queries>0</queries>
                <errors>0</errors>
                <result_rows>0</result_rows>
                <read_rows>0</read_rows>
                <execution_time>0</execution_time>
            </interval>
        </frameworks_quota>
        <readonly_quota>
            <interval>
                <duration>3600</duration>
                <queries>1000</queries>
                <errors>50</errors>
                <result_rows>10000000</result_rows>
                <read_rows>100000000</read_rows>
                <execution_time>1800</execution_time>
            </interval>
        </readonly_quota>
    </quotas>
</clickhouse>
`)
}

// BuildClickHousePasswordsDropIn returns the systemd drop-in that exposes
// the CLICKHOUSE_PASSWORD and CLICKHOUSE_READONLY_PASSWORD env vars to the
// clickhouse-server service.
func BuildClickHousePasswordsDropIn(password, readonlyPassword string) []byte {
	return fmt.Appendf(nil, `[Service]
Environment="CLICKHOUSE_PASSWORD=%s"
Environment="CLICKHOUSE_READONLY_PASSWORD=%s"
`, password, readonlyPassword)
}

func (c *ClickHouseProvisioner) Configure(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	password := metaString(config.Metadata, "clickhouse_password")
	if password == "" {
		return nil // no password configured — skip (dev mode)
	}

	readonlyPassword := metaString(config.Metadata, "clickhouse_readonly_password")
	if readonlyPassword == "" {
		readonlyPassword = password
	}

	usersXML := string(BuildClickHouseUsersXML())
	passwordsDropIn := string(BuildClickHousePasswordsDropIn(password, readonlyPassword))

	tasks := []ansible.Task{
		ansible.TaskCopy("/etc/clickhouse-server/users.xml", usersXML, ansible.CopyOpts{
			Owner: "clickhouse", Group: "clickhouse", Mode: "0640",
		}),
		{
			Name:   "ensure clickhouse-server systemd drop-in dir",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/systemd/system/clickhouse-server.service.d", "state": "directory", "mode": "0755"},
		},
		ansible.TaskCopy("/etc/systemd/system/clickhouse-server.service.d/passwords.conf", passwordsDropIn, ansible.CopyOpts{
			Mode: "0600",
		}),
		ansible.TaskSystemdService("clickhouse-server", ansible.SystemdOpts{
			State:        "restarted",
			DaemonReload: true,
		}),
	}

	playbook := &ansible.Playbook{
		Name:  "Configure ClickHouse credentials",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Configure ClickHouse credentials",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: false,
				Tasks:       tasks,
			},
		},
	}
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": c.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := c.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("failed to configure ClickHouse credentials: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("ClickHouse configuration playbook failed\nOutput: %s", result.Output)
	}

	fmt.Printf("✓ ClickHouse credentials configured on %s\n", host.ExternalIP)
	return nil
}

// ApplyDemoSeeds applies ClickHouse demo data for development.
// Uses the "frameworks" user when a password is provided in config.Metadata["clickhouse_password"],
// falling back to "default" (unauthenticated) for local/unconfigured clusters.
func (c *ClickHouseProvisioner) ApplyDemoSeeds(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	sqlContent, err := dbsql.Content.ReadFile("seeds/demo/clickhouse_demo_data.sql")
	if err != nil {
		return fmt.Errorf("read clickhouse demo seed: %w", err)
	}

	username := "default"
	password := ""
	if pw, ok := config.Metadata["clickhouse_password"].(string); ok && pw != "" {
		username = "frameworks"
		password = pw
	}

	if err := c.ch.Exec(ctx, host.ExternalIP, config.Port, username, password, "periscope", string(sqlContent)); err != nil {
		return fmt.Errorf("apply clickhouse demo seed: %w", err)
	}
	return nil
}
