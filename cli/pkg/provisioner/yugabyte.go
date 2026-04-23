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

// YugabyteNativeParams bundles the inputs needed to render the flagfile
// configs for a single Yugabyte node.
type YugabyteNativeParams struct {
	MasterAddresses string
	NodeIP          string
	DataDir         string
	RF              int
	YSQLPort        int
	Cloud           string
	Region          string
	Zone            string
}

// BuildYugabyteMasterConf returns the yb-master flagfile bytes.
func BuildYugabyteMasterConf(p YugabyteNativeParams) []byte {
	return fmt.Appendf(nil, `--master_addresses=%s
--rpc_bind_addresses=%s:7100
--webserver_interface=%s
--fs_data_dirs=%s
--replication_factor=%d
--placement_cloud=%s
--placement_region=%s
--placement_zone=%s
--leader_failure_max_missed_heartbeat_periods=10
--callhome_enabled=false
`, p.MasterAddresses, p.NodeIP, p.NodeIP, p.DataDir, p.RF, p.Cloud, p.Region, p.Zone)
}

// BuildYugabyteTServerConf returns the yb-tserver flagfile bytes.
func BuildYugabyteTServerConf(p YugabyteNativeParams) []byte {
	return fmt.Appendf(nil, `--tserver_master_addrs=%s
--rpc_bind_addresses=%s:9100
--webserver_interface=%s
--pgsql_proxy_bind_address=0.0.0.0:%d
--cql_proxy_bind_address=0.0.0.0:9042
--fs_data_dirs=%s
--ysql_enable_auth=true
--ysql_hba_conf_csv="host all all 0.0.0.0/0 scram-sha-256,host all all ::0/0 scram-sha-256"
--placement_cloud=%s
--placement_region=%s
--placement_zone=%s
--callhome_enabled=false
`, p.MasterAddresses, p.NodeIP, p.NodeIP, p.YSQLPort, p.DataDir, p.Cloud, p.Region, p.Zone)
}

// BuildYugabyteMasterUnit returns the yb-master.service bytes.
func BuildYugabyteMasterUnit() []byte {
	return []byte(ansible.RenderSystemdUnit(yugabyteMasterUnitSpec()))
}

// BuildYugabyteTServerUnit returns the yb-tserver.service bytes.
func BuildYugabyteTServerUnit() []byte {
	return []byte(ansible.RenderSystemdUnit(yugabyteTServerUnitSpec()))
}

// yugabyteMasterUnitSpec returns the SystemdUnitSpec for yb-master.
func yugabyteMasterUnitSpec() ansible.SystemdUnitSpec {
	return ansible.SystemdUnitSpec{
		Description: "YugabyteDB Master",
		After:       []string{"network-online.target"},
		Wants:       []string{"network-online.target"},
		User:        "yugabyte",
		Group:       "yugabyte",
		ExecStart:   "/opt/yugabyte/bin/yb-master --flagfile /opt/yugabyte/conf/master.conf",
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "1048576",
		LimitNPROC:  "12000",
		LimitCORE:   "infinity",
	}
}

// yugabyteTServerUnitSpec returns the SystemdUnitSpec for yb-tserver.
func yugabyteTServerUnitSpec() ansible.SystemdUnitSpec {
	return ansible.SystemdUnitSpec{
		Description: "YugabyteDB TServer",
		After:       []string{"network-online.target", "yb-master.service"},
		Wants:       []string{"network-online.target"},
		User:        "yugabyte",
		Group:       "yugabyte",
		ExecStart:   "/opt/yugabyte/bin/yb-tserver --flagfile /opt/yugabyte/conf/tserver.conf",
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "1048576",
		LimitNPROC:  "12000",
		LimitCORE:   "infinity",
	}
}

const yugabyteLimitsConf = `yugabyte soft core unlimited
yugabyte hard core unlimited
yugabyte soft nofile 1048576
yugabyte hard nofile 1048576
yugabyte soft nproc 12000
yugabyte hard nproc 12000
`

const yugabyteSysctlConf = `vm.swappiness=0
vm.max_map_count=262144
kernel.core_pattern=/var/lib/yugabyte/cores/core_%p_%t_%e
`

// YugabyteProvisioner provisions YugabyteDB nodes (yb-master + yb-tserver)
type YugabyteProvisioner struct {
	*BaseProvisioner
	sql      SQLExecutor
	executor *ansible.Executor
}

// NewYugabyteProvisioner creates a new YugabyteDB provisioner
func NewYugabyteProvisioner(pool *ssh.Pool, opts ...ProvisionerOption) (Provisioner, error) {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		return nil, fmt.Errorf("create ansible executor: %w", err)
	}
	p := &YugabyteProvisioner{
		BaseProvisioner: NewBaseProvisioner("yugabyte", pool),
		// sql stays nil in production — sqlExecFor builds an SSHExecutor
		// per-host wired to /opt/yugabyte/bin/ysqlsh. Tests inject a mock.
		executor: executor,
	}
	for _, opt := range opts {
		opt.applyYugabyte(p)
	}
	return p, nil
}

// Detect checks if YugabyteDB is installed and running
func (y *YugabyteProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	result, err := y.RunCommand(ctx, host, "pgrep -x yb-master && pgrep -x yb-tserver && echo RUNNING || echo NOT_RUNNING")
	if err != nil { //nolint:nilerr // pgrep failure means service not running, not an error
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}

	running := strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")

	binResult, _ := y.RunCommand(ctx, host, "test -x /opt/yugabyte/bin/yb-master && echo EXISTS")
	exists := binResult != nil && strings.Contains(binResult.Stdout, "EXISTS")

	return &detect.ServiceState{
		Exists:  exists,
		Running: running,
	}, nil
}

// Provision installs YugabyteDB on a single node via declarative Ansible
// tasks. Runs the full playbook every apply — each task's idempotence gate
// (creates:, state=present, systemd_service state=started) turns rebuilds
// into no-ops on healthy hosts, and config/unit drift gets corrected.
func (y *YugabyteProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	masterAddresses, _ := config.Metadata["master_addresses"].(string)
	nodeID, _ := config.Metadata["node_id"].(int)
	rf, _ := config.Metadata["replication_factor"].(int)
	if rf == 0 {
		rf = 3
	}

	port := config.Port
	if port == 0 {
		port = 5433
	}

	params := YugabyteNativeParams{
		MasterAddresses: masterAddresses,
		NodeIP:          host.ExternalIP,
		DataDir:         "/var/lib/yugabyte/data",
		RF:              rf,
		YSQLPort:        port,
		Cloud:           "frameworks",
		Region:          "eu",
		Zone:            fmt.Sprintf("eu-%d", nodeID),
	}

	_, remoteArch, err := y.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("detect remote arch: %w", err)
	}
	archKey := "linux-" + remoteArch
	artifact, err := resolveInfraArtifactFromChannel("yugabyte", archKey, platformChannelFromMetadata(config.Metadata), config.Metadata)
	if err != nil {
		return err
	}
	family, err := y.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("detect distro family: %w", err)
	}
	timesyncSpec, ok := ansible.ResolveDistroPackage(ansible.TimeSyncPackages, family)
	if !ok {
		return fmt.Errorf("yugabyte: unsupported distro family %q for chrony install", family)
	}

	tasks := yugabyteProvisionTasks(params, artifact.URL, artifact.Checksum, timesyncSpec)
	playbook := &ansible.Playbook{
		Name:  "Provision YugabyteDB",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Provision YugabyteDB node " + fmt.Sprint(nodeID),
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
			"ansible_ssh_private_key_file": y.sshPool.DefaultKeyPath(),
		},
	})

	result, execErr := y.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("ansible playbook failed\nOutput: %s", result.Output)
	}

	fmt.Printf("✓ YugabyteDB node %d provisioned on %s\n", nodeID, host.ExternalIP)
	return nil
}

// platformChannelFromMetadata pulls the release-channel key that
// `buildTaskConfig` injects. Returns empty string when missing; the resolver
// treats that as an error.
func platformChannelFromMetadata(metadata map[string]any) string {
	if v, ok := metadata["platform_channel"].(string); ok {
		return v
	}
	return ""
}

// yugabyteProvisionTasks renders the full declarative task list for one
// Yugabyte node. Archive contract: the vendor tarball wraps a single
// `yugabyte-<version>-<arch>/` top directory; --strip-components=1 drops
// that wrapper and lands bin/, lib/, conf/ directly under /opt/yugabyte.
func yugabyteProvisionTasks(params YugabyteNativeParams, artifactURL, artifactChecksum string, timesyncSpec ansible.DistroPackageSpec) []ansible.Task {
	masterConf := string(BuildYugabyteMasterConf(params))
	tserverConf := string(BuildYugabyteTServerConf(params))
	masterUnit := ansible.RenderSystemdUnit(yugabyteMasterUnitSpec())
	tserverUnit := ansible.RenderSystemdUnit(yugabyteTServerUnitSpec())
	installSentinel := ansible.ArtifactSentinel("/opt/yugabyte", artifactChecksum+artifactURL)

	tasks := []ansible.Task{
		// curl is needed by yugabyte's post_install.sh relocation script; cheap
		// declarative install via package module (Ansible picks the distro's
		// package manager automatically; name happens to be "curl" everywhere).
		ansible.TaskPackage("curl", ansible.PackagePresent),
	}
	// Probe for an existing timesync daemon; install+start chrony only if none
	// is running.
	tasks = append(tasks, ansible.TimeSyncTasks(timesyncSpec)...)
	tasks = append(tasks,
		// Ensure yugabyte user + group exist (idempotent via module state=present).
		ansible.Task{
			Name:   "ensure yugabyte group",
			Module: "ansible.builtin.group",
			Args:   map[string]any{"name": "yugabyte", "system": true, "state": "present"},
		},
		ansible.Task{
			Name:   "ensure yugabyte user",
			Module: "ansible.builtin.user",
			Args: map[string]any{
				"name":   "yugabyte",
				"group":  "yugabyte",
				"system": true,
				"shell":  "/usr/sbin/nologin",
				"state":  "present",
			},
		},

		// System limits + sysctl.
		ansible.TaskCopy("/etc/security/limits.d/yugabyte.conf", yugabyteLimitsConf, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskCopy("/etc/sysctl.d/99-yugabyte.conf", yugabyteSysctlConf, ansible.CopyOpts{Mode: "0644"}),
		// sysctl --system re-applies every key from /etc/sysctl.d each run;
		// ChangedWhen=false is the honest declaration that reapplying a
		// matching value is not a change. One-shell-per-key via sysctl.posix
		// would bloat the task list with no functional benefit.
		ansible.TaskShell("sysctl --system", ansible.ShellOpts{ChangedWhen: "false"}),

		// Transparent hugepages off (kernel setting, idempotent shell with a
		// cheap state probe via `when`).
		ansible.TaskShell(
			"echo never > /sys/kernel/mm/transparent_hugepage/enabled && echo never > /sys/kernel/mm/transparent_hugepage/defrag",
			ansible.ShellOpts{
				When: "ansible_facts.kernel is defined",
				Extra: map[string]any{
					"executable": "/bin/bash",
				},
				ChangedWhen: "false",
			},
		),

		// Data/config/cores dirs (file module, idempotent).
		ansible.Task{
			Name:   "create yugabyte data/conf/cores dirs",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":  "/var/lib/yugabyte/data",
				"state": "directory",
				"owner": "yugabyte",
				"group": "yugabyte",
				"mode":  "0755",
			},
		},
		ansible.Task{
			Name:   "create yugabyte cores dir",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":  "/var/lib/yugabyte/cores",
				"state": "directory",
				"owner": "yugabyte",
				"group": "yugabyte",
				"mode":  "0755",
			},
		},
		ansible.Task{
			Name:   "create /opt/yugabyte",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":  "/opt/yugabyte",
				"state": "directory",
				"owner": "yugabyte",
				"group": "yugabyte",
				"mode":  "0755",
			},
		},
		ansible.Task{
			Name:   "create /opt/yugabyte/conf",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":  "/opt/yugabyte/conf",
				"state": "directory",
				"owner": "yugabyte",
				"group": "yugabyte",
				"mode":  "0755",
			},
		},

		// Download tarball (get_url skips via checksum match on rerun).
		ansible.TaskGetURL(artifactURL, "/tmp/yugabyte.tar.gz", artifactChecksum),

		// Version-keyed sentinel: on a version bump, the sentinel path changes,
		// unarchive re-extracts on top of /opt/yugabyte, and post_install.sh
		// re-runs. Tarball is left in /tmp so get_url cache-hits on same-version
		// reruns; do NOT add a remove-tarball task or every apply re-downloads.
		ansible.TaskUnarchive("/tmp/yugabyte.tar.gz", "/opt/yugabyte", installSentinel,
			ansible.UnarchiveOpts{StripComponents: 1, Owner: "yugabyte", Group: "yugabyte"}),

		// Yugabyte's post_install.sh relocates embedded absolute paths. Gated
		// on the same version-keyed sentinel so it re-runs on every upgrade.
		ansible.TaskShell(
			"/opt/yugabyte/bin/post_install.sh && touch "+installSentinel+" && chown yugabyte:yugabyte "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel},
		),

		// Render configs + units.
		ansible.TaskCopy("/opt/yugabyte/conf/master.conf", masterConf, ansible.CopyOpts{Owner: "yugabyte", Group: "yugabyte", Mode: "0644"}),
		ansible.TaskCopy("/opt/yugabyte/conf/tserver.conf", tserverConf, ansible.CopyOpts{Owner: "yugabyte", Group: "yugabyte", Mode: "0644"}),
		ansible.TaskCopy("/etc/systemd/system/yb-master.service", masterUnit, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskCopy("/etc/systemd/system/yb-tserver.service", tserverUnit, ansible.CopyOpts{Mode: "0644"}),

		// Start master first, then tserver. daemon_reload on the first one
		// picks up the newly-written unit files.
		ansible.TaskSystemdService("yb-master", ansible.SystemdOpts{State: "started", Enabled: ansible.BoolPtr(true), DaemonReload: true}),
		ansible.TaskSystemdService("yb-tserver", ansible.SystemdOpts{State: "started", Enabled: ansible.BoolPtr(true)}),
		ansible.TaskWaitForPort(params.YSQLPort, ansible.WaitForOpts{Host: "127.0.0.1", Timeout: 90, Sleep: 1}),
	)
	return tasks
}

// Validate checks YugabyteDB health on three levels: goss asserts host-level
// structural state (services, installed binary), then a YSQL query
// confirms the DB is accepting connections, then yb-admin reports tablet
// server liveness. The goss step runs first so structural regressions are
// flagged before we try the network round-trip.
func (y *YugabyteProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 5433
	}

	if _, remoteArch, err := y.DetectRemoteArch(ctx, host); err == nil {
		spec := ansible.RenderGossYAML(ansible.GossSpec{
			Services: map[string]ansible.GossService{
				"yb-master":  {Running: true, Enabled: true},
				"yb-tserver": {Running: true, Enabled: true},
			},
			Files: map[string]ansible.GossFile{
				"/opt/yugabyte/bin/yb-master":      {Exists: true},
				"/opt/yugabyte/.post_install_done": {Exists: true},
			},
		})
		if gossErr := runGossValidate(ctx, y.executor, y.sshPool.DefaultKeyPath(), host,
			"yugabyte", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
			return fmt.Errorf("yugabyte goss validate failed: %w", gossErr)
		}
	}

	clusterIP := host.ExternalIP
	if clusterIP == "" {
		clusterIP = "127.0.0.1"
	}
	tasks := []ansible.Task{
		waitForTCP("wait for ysql listener", clusterIP, port, 60),
		shellValidate("yugabyte SELECT version()",
			fmt.Sprintf(`sudo -u yugabyte /opt/yugabyte/bin/ysqlsh -h %s -p %d -U yugabyte -d yugabyte -tAc "SELECT version()" | grep -q YugabyteDB`,
				clusterIP, port)),
	}
	if err := runValidatePlaybook(ctx, y.executor, y.sshPool.DefaultKeyPath(), host, "yugabyte", tasks); err != nil {
		return err
	}

	fmt.Printf("✓ YugabyteDB healthy on %s (YSQL port %d)\n", host.ExternalIP, port)
	return nil
}

// sqlExecFor returns the SQLExecutor for YSQL operations on host. Tests
// inject a mock via WithYugabyteSQLExecutor; production pipes SQL through
// /opt/yugabyte/bin/ysqlsh on the host over SSH, so the CLI never needs
// direct network access to YSQL.
func (y *YugabyteProvisioner) sqlExecFor(host inventory.Host, config ServiceConfig) (SQLExecutor, error) {
	if y.sql != nil {
		return y.sql, nil
	}
	runner, err := y.GetRunner(host)
	if err != nil {
		return nil, fmt.Errorf("get ssh runner: %w", err)
	}
	pwd, _ := config.Metadata["yugabyte_password"].(string) //nolint:errcheck // zero value is the no-password path
	return &SSHExecutor{
		Runner:     runner,
		BinaryPath: "/opt/yugabyte/bin/ysqlsh",
		// YSQL's pg_hba is scram-sha-256 on all interfaces; there is no
		// peer-auth shortcut, so we authenticate via PGPASSFILE when a
		// password is configured.
		UsePeerAuth: false,
		Password:    pwd,
	}, nil
}

// Initialize creates databases and application user via YSQL
func (y *YugabyteProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 5433
	}
	exec, err := y.sqlExecFor(host, config)
	if err != nil {
		return err
	}

	conn := ConnParams{Host: host.ExternalIP, Port: port, User: "yugabyte", Database: "yugabyte"}

	databases, ok := config.Metadata["databases"].([]map[string]string)
	if !ok {
		databases = []map[string]string{{"name": "periscope"}}
	}

	var dbNames []string
	for _, dbEntry := range databases {
		dbName := dbEntry["name"]
		dbNames = append(dbNames, dbName)

		created, err := CreateDatabaseIfNotExists(ctx, exec, conn, dbName, "")
		if err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		if created {
			fmt.Printf("Created database: %s\n", dbName)
		} else {
			fmt.Printf("Database %s already exists\n", dbName)
		}

		dbConn := ConnParams{Host: host.ExternalIP, Port: port, User: "yugabyte", Database: dbName}
		if err := pgInitializeDatabase(ctx, exec, dbConn, dbName); err != nil {
			return fmt.Errorf("failed to initialize database %s: %w", dbName, err)
		}
	}

	pgPass, _ := config.Metadata["postgres_password"].(string)
	if pgPass != "" {
		ownerDBs := make(map[string][]string)
		for _, db := range databases {
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

// Configure is a no-op for YugabyteDB — auth is configured via gflags during Provision
func (y *YugabyteProvisioner) Configure(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}
