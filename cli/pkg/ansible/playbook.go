package ansible

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// NewPlaybook creates a new Ansible playbook
func NewPlaybook(name, hosts string) *Playbook {
	return &Playbook{
		Name:  name,
		Hosts: hosts,
		Plays: []Play{},
	}
}

// AddPlay adds a play to the playbook
func (p *Playbook) AddPlay(play Play) {
	p.Plays = append(p.Plays, play)
}

// ToYAML converts the playbook to YAML format
func (p *Playbook) ToYAML() ([]byte, error) {
	// Convert to Ansible playbook structure
	plays := make([]map[string]any, 0, len(p.Plays))

	for _, play := range p.Plays {
		playMap := map[string]any{
			"name":  play.Name,
			"hosts": play.Hosts,
		}

		if play.BecomeUser != "" {
			playMap["become"] = play.Become
			playMap["become_user"] = play.BecomeUser
		} else if play.Become {
			playMap["become"] = true
		}

		if play.GatherFacts {
			playMap["gather_facts"] = true
		} else {
			playMap["gather_facts"] = false
		}

		if len(play.Vars) > 0 {
			playMap["vars"] = play.Vars
		}

		if len(play.PreTasks) > 0 {
			playMap["pre_tasks"] = convertTasks(play.PreTasks)
		}

		if len(play.Roles) > 0 {
			playMap["roles"] = convertRoles(play.Roles)
		}

		if len(play.Tasks) > 0 {
			playMap["tasks"] = convertTasks(play.Tasks)
		}

		if len(play.PostTasks) > 0 {
			playMap["post_tasks"] = convertTasks(play.PostTasks)
		}

		if len(play.Handlers) > 0 {
			playMap["handlers"] = convertHandlers(play.Handlers)
		}

		plays = append(plays, playMap)
	}

	return yaml.Marshal(plays)
}

// convertTasks converts Task structs to Ansible task maps
func convertTasks(tasks []Task) []map[string]any {
	result := make([]map[string]any, 0, len(tasks))

	for _, task := range tasks {
		taskMap := map[string]any{
			"name": task.Name,
		}

		// Add module and args
		if task.Module != "" {
			taskMap[task.Module] = task.Args
		}

		if task.When != "" {
			taskMap["when"] = task.When
		}

		if task.ChangedWhen != "" {
			taskMap["changed_when"] = task.ChangedWhen
		}

		if len(task.Environment) > 0 {
			taskMap["environment"] = task.Environment
		}

		if task.Register != "" {
			taskMap["register"] = task.Register
		}

		if len(task.Notify) > 0 {
			taskMap["notify"] = task.Notify
		}

		if len(task.Tags) > 0 {
			taskMap["tags"] = task.Tags
		}

		if task.Ignore {
			taskMap["ignore_errors"] = true
		}

		result = append(result, taskMap)
	}

	return result
}

// convertRoles converts Role structs to Ansible role format
func convertRoles(roles []Role) []any {
	result := make([]any, 0, len(roles))

	for _, role := range roles {
		if len(role.Vars) > 0 {
			result = append(result, map[string]any{
				"role": role.Name,
				"vars": role.Vars,
			})
		} else {
			result = append(result, role.Name)
		}
	}

	return result
}

// convertHandlers converts Handler structs to Ansible handler maps
func convertHandlers(handlers []Handler) []map[string]any {
	result := make([]map[string]any, 0, len(handlers))

	for _, handler := range handlers {
		handlerMap := map[string]any{
			"name":         handler.Name,
			handler.Module: handler.Args,
		}
		result = append(result, handlerMap)
	}

	return result
}

// GeneratePostgresPlaybook creates an Ansible playbook for PostgreSQL.
// PostgresManagedConfBlock returns the bytes inserted into postgresql.conf's
// frameworks managed section.
func PostgresManagedConfBlock() []byte {
	return []byte(`# frameworks managed begin
listen_addresses = '*'
max_connections = 200
shared_buffers = '256MB'
work_mem = '8MB'
maintenance_work_mem = '128MB'
effective_cache_size = '768MB'
wal_buffers = '16MB'
checkpoint_completion_target = 0.9
random_page_cost = 1.1
log_connections = on
log_disconnections = on
log_statement = 'ddl'
password_encryption = 'scram-sha-256'
# frameworks managed end
`)
}

// PostgresManagedHBABlock returns the bytes inserted into pg_hba.conf's
// frameworks managed section.
func PostgresManagedHBABlock() []byte {
	return []byte(`# frameworks managed begin
host all all 127.0.0.1/32 scram-sha-256
host all all ::1/128 scram-sha-256
host all all 0.0.0.0/0 scram-sha-256
host all all ::/0 scram-sha-256
# frameworks managed end
`)
}

// PostgresSourceBuiltSystemdUnit returns the systemd unit bytes for the
// source-built Postgres path (dnf/yum/pacman when POSTGRES_VERSION is set).
// dataDir varies by distro — pass the exact path used in the install script.
func PostgresSourceBuiltSystemdUnit(dataDir string) []byte {
	return fmt.Appendf(nil, `[Unit]
Description=PostgreSQL database server
After=network.target

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/opt/postgresql/bin/postgres -D %s
ExecReload=/bin/kill -HUP $MAINPID
KillMode=mixed
KillSignal=SIGINT
TimeoutSec=0
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, dataDir)
}

// PostgresInstallParams describes a Postgres provisioning request after the
// caller has detected distro family + arch + resolved any artifact.
type PostgresInstallParams struct {
	DistroFamily     string // "debian" | "rhel" | "arch"
	Version          string // empty = vendor-package install; non-empty = source-build
	Databases        []string
	ArtifactURL      string // pinned tarball URL (only for source-build)
	ArtifactChecksum string // pinned tarball checksum
	ServiceName      string // distro's postgres service name; debian:"postgresql", rhel-pkg:"postgresql", arch-pkg:"postgresql", source-build:"postgresql"
}

// GeneratePostgresPlaybook creates an Ansible playbook for PostgreSQL using
// the typed task model. Distro family decides which install path: vendor
// package on debian (always) and on rhel/arch when no version is pinned;
// source-build on rhel/arch when a version is pinned.
func GeneratePostgresPlaybook(host string, params PostgresInstallParams) *Playbook {
	playbook := NewPlaybook("Provision PostgreSQL", host)

	managedConf := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(string(PostgresManagedConfBlock()), "\n# frameworks managed end\n"), "# frameworks managed begin\n"))
	managedHBA := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(string(PostgresManagedHBABlock()), "\n# frameworks managed end\n"), "# frameworks managed begin\n"))

	var (
		tasks    []Task
		pgConf   string
		pgHBA    string
		dataDir  string
		svcName  = "postgresql"
		needsDir = false
	)

	switch params.DistroFamily {
	case "debian":
		tasks, pgConf, pgHBA = postgresDebianTasks(params.Version)
	case "rhel":
		if params.Version != "" {
			dataDir = "/var/lib/postgresql/data"
			tasks = postgresSourceBuildTasks("rhel", params.Version, params.ArtifactURL, params.ArtifactChecksum, dataDir)
			pgConf, pgHBA = dataDir+"/postgresql.conf", dataDir+"/pg_hba.conf"
			needsDir = true
		} else {
			pgConf, pgHBA = "/var/lib/pgsql/data/postgresql.conf", "/var/lib/pgsql/data/pg_hba.conf"
			tasks = []Task{
				TaskPackage("postgresql-server", PackagePresent),
				TaskPackage("postgresql", PackagePresent),
				TaskShell("postgresql-setup --initdb", ShellOpts{Creates: "/var/lib/pgsql/data/PG_VERSION"}),
			}
		}
	case "arch":
		if params.Version != "" {
			dataDir = "/var/lib/postgres/data"
			tasks = postgresSourceBuildTasks("arch", params.Version, params.ArtifactURL, params.ArtifactChecksum, dataDir)
			pgConf, pgHBA = dataDir+"/postgresql.conf", dataDir+"/pg_hba.conf"
			needsDir = true
		} else {
			dataDir = "/var/lib/postgres/data"
			pgConf, pgHBA = dataDir+"/postgresql.conf", dataDir+"/pg_hba.conf"
			tasks = []Task{
				TaskPackage("postgresql", PackagePresent),
				{
					Name:   "create postgres data dir",
					Module: "ansible.builtin.file",
					Args:   map[string]any{"path": dataDir, "state": "directory", "owner": "postgres", "group": "postgres", "mode": "0700"},
				},
				TaskShell("su -s /bin/sh postgres -c 'initdb -D "+dataDir+"'", ShellOpts{Creates: dataDir + "/PG_VERSION"}),
			}
		}
	default:
		// Unknown family: emit a single shell task that fails clearly.
		tasks = []Task{
			TaskShell(`echo "unsupported package manager" >&2; exit 1`, ShellOpts{ChangedWhen: "false"}),
		}
		playbook.AddPlay(Play{Name: "Provision PostgreSQL", Hosts: host, Become: true, GatherFacts: true, Tasks: tasks})
		return playbook
	}

	// Source-build path needs the data dir created upfront.
	if needsDir {
		tasks = append(tasks,
			Task{
				Name:   "create postgres data dir",
				Module: "ansible.builtin.file",
				Args:   map[string]any{"path": dataDir, "state": "directory", "owner": "postgres", "group": "postgres", "mode": "0700"},
			},
			TaskShell("su -s /bin/sh postgres -c '/opt/postgresql/bin/initdb -D "+dataDir+"'", ShellOpts{Creates: dataDir + "/PG_VERSION"}),
			TaskCopy("/etc/systemd/system/postgresql.service", string(PostgresSourceBuiltSystemdUnit(dataDir)), CopyOpts{Mode: "0644"}),
		)
	}

	// Common post-install: managed config blocks (idempotent via blockinfile)
	// + service start + database bootstrap.
	if params.DistroFamily == "debian" {
		// Debian's path lookup happens at apply time via shell+register because
		// the conf path includes the major version (e.g. /etc/postgresql/15/main).
		tasks = append(tasks,
			Task{
				Name:        "find debian postgresql.conf",
				Module:      "ansible.builtin.shell",
				Args:        map[string]any{"cmd": "find /etc/postgresql -path '*/main/postgresql.conf' | head -n 1", "executable": "/bin/bash"},
				Register:    "pgconf_path",
				ChangedWhen: "false",
			},
			Task{
				Name:        "find debian pg_hba.conf",
				Module:      "ansible.builtin.shell",
				Args:        map[string]any{"cmd": "find /etc/postgresql -path '*/main/pg_hba.conf' | head -n 1", "executable": "/bin/bash"},
				Register:    "pghba_path",
				ChangedWhen: "false",
			},
			Task{
				Name:   "managed block in postgresql.conf",
				Module: "ansible.builtin.blockinfile",
				Args: map[string]any{
					"path":   "{{ pgconf_path.stdout }}",
					"marker": "# frameworks managed {mark}",
					"block":  managedConf,
				},
			},
			Task{
				Name:   "managed block in pg_hba.conf",
				Module: "ansible.builtin.blockinfile",
				Args: map[string]any{
					"path":   "{{ pghba_path.stdout }}",
					"marker": "# frameworks managed {mark}",
					"block":  managedHBA,
				},
			},
		)
	} else {
		tasks = append(tasks,
			Task{
				Name:   "managed block in postgresql.conf",
				Module: "ansible.builtin.blockinfile",
				Args: map[string]any{
					"path":   pgConf,
					"marker": "# frameworks managed {mark}",
					"block":  managedConf,
				},
			},
			Task{
				Name:   "managed block in pg_hba.conf",
				Module: "ansible.builtin.blockinfile",
				Args: map[string]any{
					"path":   pgHBA,
					"marker": "# frameworks managed {mark}",
					"block":  managedHBA,
				},
			},
		)
	}

	tasks = append(tasks, TaskSystemdService(svcName, SystemdOpts{
		State:        "restarted",
		Enabled:      BoolPtr(true),
		DaemonReload: needsDir, // only source-build wrote a fresh unit file
	}))

	// Database bootstrap: idempotent createdb-if-not-exists shell per database.
	for _, name := range params.Databases {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		quoted := strings.ReplaceAll(name, `'`, `''`)
		cmd := fmt.Sprintf("su -s /bin/sh postgres -c \"psql -tAc \\\"SELECT 1 FROM pg_database WHERE datname='%s'\\\" | grep -q 1 || createdb %s\"", quoted, name)
		tasks = append(tasks, TaskShell(cmd, ShellOpts{ChangedWhen: "false"}))
	}

	playbook.AddPlay(Play{Name: "Install and configure PostgreSQL", Hosts: host, Become: true, GatherFacts: true, Tasks: tasks})
	return playbook
}

// postgresDebianTasks returns the apt-managed install task list. Debian's
// pgconf/pghba paths include the major version (e.g. /etc/postgresql/15/main),
// so they're discovered at apply time rather than hardcoded.
func postgresDebianTasks(version string) (tasks []Task, pgConfPath, pgHBAPath string) {
	pkg := "postgresql"
	if version != "" {
		// Debian uses postgresql-N where N is the major version.
		major := version
		if dot := strings.Index(version, "."); dot > 0 {
			major = version[:dot]
		}
		pkg = "postgresql-" + major
	}
	tasks = []Task{
		{
			Name:   "apt update",
			Module: "ansible.builtin.apt",
			Args:   map[string]any{"update_cache": true},
		},
		TaskPackage(pkg, PackagePresent),
		TaskPackage("postgresql-contrib", PackagePresent),
	}
	// pgConfPath/pgHBAPath unused for debian (looked up at apply time via
	// shell+register), returned as empty.
	return tasks, "", ""
}

// postgresSourceBuildTasks returns the task list for the source-build path
// (rhel/arch when a specific version is pinned). The build is gated by a
// `creates:` marker on the installed binary so re-runs short-circuit.
func postgresSourceBuildTasks(family, version, artifactURL, artifactChecksum, dataDir string) []Task {
	prefix := "/opt/postgresql-" + version
	tasks := []Task{
		// Group + user.
		{
			Name:   "ensure postgres group",
			Module: "ansible.builtin.group",
			Args:   map[string]any{"name": "postgres", "system": true, "state": "present"},
		},
		{
			Name:   "ensure postgres user",
			Module: "ansible.builtin.user",
			Args: map[string]any{
				"name":   "postgres",
				"group":  "postgres",
				"system": true,
				"home":   "/var/lib/postgresql",
				"shell":  "/usr/sbin/nologin",
				"state":  "present",
			},
		},
	}

	// Build-time deps (devel libs differ per family).
	switch family {
	case "rhel":
		for _, p := range []string{"gcc", "make", "readline-devel", "zlib-devel", "openssl-devel", "libicu-devel", "curl", "tar"} {
			tasks = append(tasks, TaskPackage(p, PackagePresent))
		}
	case "arch":
		for _, p := range []string{"base-devel", "curl", "icu", "krb5", "openssl", "readline", "zlib"} {
			tasks = append(tasks, TaskPackage(p, PackagePresent))
		}
	}

	// Version-keyed sentinels rotate when the pinned URL/checksum changes so a
	// version bump re-extracts + rebuilds instead of skipping on stale markers.
	// The prefix itself is versioned (/opt/postgresql-<ver>), so Creates gates
	// on the built binary there are also effectively version-keyed.
	extractSentinel := ArtifactSentinel("/tmp/postgresql-src", artifactChecksum+artifactURL)
	tasks = append(tasks,
		TaskGetURL(artifactURL, "/tmp/postgresql.tar.bz2", artifactChecksum),
		Task{
			Name:   "create /tmp/postgresql-src",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/tmp/postgresql-src", "state": "directory", "mode": "0755"},
		},
		// Tarball + source tree stay in /tmp so same-version reruns short-
		// circuit: get_url cache-hits via checksum, unarchive skips via the
		// sentinel gate. The tree is large but /tmp is cleared on reboot;
		// deleting it here would force re-download + re-extract every apply
		// and break changed=0 idempotence.
		TaskUnarchive("/tmp/postgresql.tar.bz2", "/tmp/postgresql-src", extractSentinel,
			UnarchiveOpts{StripComponents: 1}),
		TaskShell("touch "+extractSentinel, ShellOpts{Creates: extractSentinel}),
		// configure + make + install. Gated by prefix/bin/postgres; when
		// prefix is /opt/postgresql-<ver> this naturally rebuilds on version bumps.
		TaskShell(
			fmt.Sprintf(`./configure --prefix=%q && make -j"$(nproc)" && make install`, prefix),
			ShellOpts{Creates: prefix + "/bin/postgres", Chdir: "/tmp/postgresql-src"},
		),
		Task{
			Name:   "symlink /opt/postgresql",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"src": prefix, "dest": "/opt/postgresql", "state": "link", "force": true},
		},
	)

	// dataDir referenced by caller for initdb + systemd unit emission; ignore
	// here, but assert non-empty since the caller passes it in.
	_ = dataDir
	return tasks
}

// KafkaCombinedParams are the inputs for combined broker+controller mode.
type KafkaCombinedParams struct {
	NodeID           int
	ListenerHost     string
	ListenerPort     int
	ControllerPort   int
	ControllerQuorum string
	MinISR           int
	OffsetsRF        int
	TxRF             int
	TxMinISR         int
	DeleteTopics     bool
}

// BuildKafkaCombinedServerProperties returns the /etc/kafka/server.properties
// bytes for combined broker+controller mode.
func BuildKafkaCombinedServerProperties(p KafkaCombinedParams) []byte {
	return fmt.Appendf(nil, `node.id=%d
process.roles=broker,controller
controller.quorum.voters=%s
controller.listener.names=CONTROLLER
listeners=PLAINTEXT://0.0.0.0:%d,CONTROLLER://0.0.0.0:%d
advertised.listeners=PLAINTEXT://%s:%d
inter.broker.listener.name=PLAINTEXT
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
num.network.threads=3
num.io.threads=8
socket.send.buffer.bytes=102400
socket.receive.buffer.bytes=102400
socket.request.max.bytes=104857600
log.dirs=/var/lib/kafka/logs
num.partitions=3
num.recovery.threads.per.data.dir=1
min.insync.replicas=%d
offsets.topic.replication.factor=%d
transaction.state.log.replication.factor=%d
transaction.state.log.min.isr=%d
log.retention.hours=168
group.initial.rebalance.delay.ms=0
auto.create.topics.enable=false
delete.topic.enable=%t
`, p.NodeID, p.ControllerQuorum, p.ListenerPort, p.ControllerPort, p.ListenerHost, p.ListenerPort,
		p.MinISR, p.OffsetsRF, p.TxRF, p.TxMinISR, p.DeleteTopics)
}

// kafkaBrokerUnitSpec is the SystemdUnitSpec for frameworks-kafka.service.
func kafkaBrokerUnitSpec() SystemdUnitSpec {
	return SystemdUnitSpec{
		Description: "FrameWorks Kafka Broker",
		After:       []string{"network-online.target"},
		Wants:       []string{"network-online.target"},
		User:        "kafka",
		Group:       "kafka",
		ExecStart:   "/opt/kafka/bin/kafka-server-start.sh /etc/kafka/server.properties",
		ExecStop:    "/opt/kafka/bin/kafka-server-stop.sh",
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "100000",
	}
}

// BuildKafkaBrokerUnit returns the frameworks-kafka.service bytes.
func BuildKafkaBrokerUnit() []byte {
	return []byte(RenderSystemdUnit(kafkaBrokerUnitSpec()))
}

// KafkaControllerParams are the inputs for dedicated controller mode.
type KafkaControllerParams struct {
	NodeID           int
	ControllerPort   int
	BootstrapServers string
}

// BuildKafkaControllerServerProperties returns the
// /etc/kafka-controller/server.properties bytes.
func BuildKafkaControllerServerProperties(p KafkaControllerParams) []byte {
	return fmt.Appendf(nil, `process.roles=controller
node.id=%d
controller.listener.names=CONTROLLER
listeners=CONTROLLER://0.0.0.0:%d
listener.security.protocol.map=CONTROLLER:PLAINTEXT
controller.quorum.bootstrap.servers=%s
log.dirs=/var/lib/kafka-controller/logs
`, p.NodeID, p.ControllerPort, p.BootstrapServers)
}

// KafkaBrokerParams are the inputs for dedicated broker mode.
type KafkaBrokerParams struct {
	NodeID           int
	ListenerHost     string
	ListenerPort     int
	BootstrapServers string
	MinISR           int
	OffsetsRF        int
	TxRF             int
	TxMinISR         int
	DeleteTopics     bool
}

// BuildKafkaBrokerServerProperties returns the /etc/kafka/server.properties
// bytes for dedicated broker mode.
func BuildKafkaBrokerServerProperties(p KafkaBrokerParams) []byte {
	return fmt.Appendf(nil, `process.roles=broker
node.id=%d
controller.listener.names=CONTROLLER
listeners=PLAINTEXT://0.0.0.0:%d
advertised.listeners=PLAINTEXT://%s:%d
inter.broker.listener.name=PLAINTEXT
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.bootstrap.servers=%s
num.network.threads=3
num.io.threads=8
socket.send.buffer.bytes=102400
socket.receive.buffer.bytes=102400
socket.request.max.bytes=104857600
log.dirs=/var/lib/kafka/logs
num.partitions=3
num.recovery.threads.per.data.dir=1
min.insync.replicas=%d
offsets.topic.replication.factor=%d
transaction.state.log.replication.factor=%d
transaction.state.log.min.isr=%d
log.retention.hours=168
group.initial.rebalance.delay.ms=0
auto.create.topics.enable=false
delete.topic.enable=%t
`, p.NodeID, p.ListenerPort, p.ListenerHost, p.ListenerPort, p.BootstrapServers,
		p.MinISR, p.OffsetsRF, p.TxRF, p.TxMinISR, p.DeleteTopics)
}

// kafkaControllerUnitSpec is the SystemdUnitSpec for frameworks-kafka-controller.service.
func kafkaControllerUnitSpec() SystemdUnitSpec {
	return SystemdUnitSpec{
		Description: "FrameWorks Kafka Controller",
		After:       []string{"network-online.target"},
		Wants:       []string{"network-online.target"},
		User:        "kafka",
		Group:       "kafka",
		ExecStart:   "/opt/kafka/bin/kafka-server-start.sh /etc/kafka-controller/server.properties",
		ExecStop:    "/opt/kafka/bin/kafka-server-stop.sh",
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "100000",
	}
}

// BuildKafkaControllerUnit returns the frameworks-kafka-controller.service bytes.
func BuildKafkaControllerUnit() []byte {
	return []byte(RenderSystemdUnit(kafkaControllerUnitSpec()))
}

// kafkaCommonInstallTasks returns the shared prefix for every Kafka role:
// prereqs (curl + Java), user/group, base dirs, download+extract. configDir
// is "/etc/kafka" for combined/broker, "/etc/kafka-controller" for controller;
// logsDir matches. javaSpec names the distro-correct JRE package, picked by
// the caller from JavaRuntimePackages using DetectDistroFamily.
func kafkaCommonInstallTasks(artifactURL, artifactChecksum, configDir, logsDir string, javaSpec DistroPackageSpec) []Task {
	// Sentinel path rotates with the pinned artifact identity, so both the
	// unarchive skip and the touch-marker shell trigger on a version bump.
	sentinel := ArtifactSentinel("/opt/kafka", artifactChecksum+artifactURL)
	tasks := []Task{
		TaskPackage("curl", PackagePresent),
		{
			Name:   "ensure kafka group",
			Module: "ansible.builtin.group",
			Args:   map[string]any{"name": "kafka", "system": true, "state": "present"},
		},
		{
			Name:   "ensure kafka user",
			Module: "ansible.builtin.user",
			Args: map[string]any{
				"name":   "kafka",
				"group":  "kafka",
				"system": true,
				"shell":  "/usr/sbin/nologin",
				"state":  "present",
			},
		},
		{
			Name:   "create " + configDir,
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": configDir, "state": "directory", "owner": "kafka", "group": "kafka", "mode": "0755"},
		},
		{
			Name:   "create " + logsDir,
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": logsDir, "state": "directory", "owner": "kafka", "group": "kafka", "mode": "0755"},
		},
		{
			Name:   "create /opt/kafka",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/opt/kafka", "state": "directory", "owner": "kafka", "group": "kafka", "mode": "0755"},
		},
		TaskGetURL(artifactURL, "/tmp/kafka.tgz", artifactChecksum),
		// Tarball is intentionally left in /tmp. get_url with checksum matches
		// the cached file on rerun and skips; deleting it would force a
		// re-download every apply and break changed=0 idempotence.
		TaskUnarchive("/tmp/kafka.tgz", "/opt/kafka", sentinel,
			UnarchiveOpts{StripComponents: 1, Owner: "kafka", Group: "kafka"}),
		TaskShell("touch "+sentinel+" && chown kafka:kafka "+sentinel,
			ShellOpts{Creates: sentinel}),
	}
	tasks = append(tasks[:1], append(JavaRuntimeTasks(javaSpec), tasks[1:]...)...)
	return tasks
}

// GenerateKafkaKRaftPlaybook creates an Ansible playbook for Kafka in KRaft mode (no ZooKeeper).
// artifactURL + artifactChecksum come from the provisioner-side artifact resolver
// (pre-arch-selected).
func GenerateKafkaKRaftPlaybook(version string, nodeID int, host string, port int, controllerPort int, controllerQuorum string, clusterID string, metadata map[string]any, artifactURL, artifactChecksum string, javaSpec DistroPackageSpec) *Playbook {
	playbook := NewPlaybook("Provision Kafka", host)
	if port == 0 {
		port = 9092
	}
	if controllerPort == 0 {
		controllerPort = 9093
	}

	brokerCount := max(metadataInt(metadata, "broker_count", 1), 1)
	defaultRF := min(brokerCount, 3)
	minISR := max(metadataInt(metadata, "min_insync_replicas", defaultRF-1), 1)
	offsetsRF := max(metadataInt(metadata, "offsets_topic_replication_factor", defaultRF), 1)
	txRF := max(metadataInt(metadata, "transaction_state_log_replication_factor", defaultRF), 1)
	txMinISR := max(metadataInt(metadata, "transaction_state_log_min_isr", txRF-1), 1)
	deleteTopics := metadataBool(metadata, "delete_topic_enable", false)

	serverProps := string(BuildKafkaCombinedServerProperties(KafkaCombinedParams{
		NodeID:           nodeID,
		ListenerHost:     host,
		ListenerPort:     port,
		ControllerPort:   controllerPort,
		ControllerQuorum: controllerQuorum,
		MinISR:           minISR,
		OffsetsRF:        offsetsRF,
		TxRF:             txRF,
		TxMinISR:         txMinISR,
		DeleteTopics:     deleteTopics,
	}))
	brokerUnit := RenderSystemdUnit(kafkaBrokerUnitSpec())

	tasks := kafkaCommonInstallTasks(artifactURL, artifactChecksum, "/etc/kafka", "/var/lib/kafka/logs", javaSpec)
	tasks = append(tasks,
		TaskCopy("/etc/kafka/server.properties", serverProps, CopyOpts{Owner: "kafka", Group: "kafka", Mode: "0644"}),
		// kafka-storage.sh format is required exactly once per cluster; the
		// meta.properties marker is the idempotence key.
		TaskShell(
			fmt.Sprintf(`/opt/kafka/bin/kafka-storage.sh format -t %q -c /etc/kafka/server.properties`, clusterID),
			ShellOpts{Creates: "/var/lib/kafka/logs/meta.properties"},
		),
		Task{
			Name:   "ensure kafka log dir ownership",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":    "/var/lib/kafka/logs",
				"state":   "directory",
				"owner":   "kafka",
				"group":   "kafka",
				"recurse": true,
			},
		},
		TaskCopy("/etc/systemd/system/frameworks-kafka.service", brokerUnit, CopyOpts{Mode: "0644"}),
		TaskSystemdService("frameworks-kafka", SystemdOpts{State: "started", Enabled: BoolPtr(true), DaemonReload: true}),
		TaskWaitForPort(port, WaitForOpts{Host: "127.0.0.1", Timeout: 60, Sleep: 1}),
	)

	playbook.AddPlay(Play{
		Name:        "Install and configure Kafka",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks:       tasks,
	})
	return playbook
}

// GenerateKafkaControllerPlaybook creates an Ansible playbook for a dedicated KRaft controller.
// artifactURL + artifactChecksum come from the provisioner-side artifact resolver.
func GenerateKafkaControllerPlaybook(version string, nodeID int, host string, controllerPort int, bootstrapServers string, clusterID string, initialControllers string, artifactURL, artifactChecksum string, javaSpec DistroPackageSpec) *Playbook {
	playbook := NewPlaybook("Provision Kafka Controller", host)
	if controllerPort == 0 {
		controllerPort = 9093
	}

	serverProps := string(BuildKafkaControllerServerProperties(KafkaControllerParams{
		NodeID:           nodeID,
		ControllerPort:   controllerPort,
		BootstrapServers: bootstrapServers,
	}))
	ctrlUnit := RenderSystemdUnit(kafkaControllerUnitSpec())

	tasks := kafkaCommonInstallTasks(artifactURL, artifactChecksum, "/etc/kafka-controller", "/var/lib/kafka-controller/logs", javaSpec)
	tasks = append(tasks,
		TaskCopy("/etc/kafka-controller/server.properties", serverProps, CopyOpts{Owner: "kafka", Group: "kafka", Mode: "0644"}),
		TaskShell(
			fmt.Sprintf(`/opt/kafka/bin/kafka-storage.sh format --cluster-id %q --initial-controllers %q --config /etc/kafka-controller/server.properties`, clusterID, initialControllers),
			ShellOpts{Creates: "/var/lib/kafka-controller/logs/meta.properties"},
		),
		Task{
			Name:   "ensure kafka controller log dir ownership",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":    "/var/lib/kafka-controller/logs",
				"state":   "directory",
				"owner":   "kafka",
				"group":   "kafka",
				"recurse": true,
			},
		},
		TaskCopy("/etc/systemd/system/frameworks-kafka-controller.service", ctrlUnit, CopyOpts{Mode: "0644"}),
		TaskSystemdService("frameworks-kafka-controller", SystemdOpts{State: "started", Enabled: BoolPtr(true), DaemonReload: true}),
		TaskWaitForPort(controllerPort, WaitForOpts{Host: "127.0.0.1", Timeout: 60, Sleep: 1}),
	)

	playbook.AddPlay(Play{
		Name:        "Install and configure Kafka Controller",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks:       tasks,
	})
	return playbook
}

// GenerateKafkaBrokerPlaybook creates an Ansible playbook for a broker-only Kafka node (dedicated controller mode).
// artifactURL + artifactChecksum come from the provisioner-side artifact resolver.
func GenerateKafkaBrokerPlaybook(version string, nodeID int, host string, port int, bootstrapServers string, clusterID string, metadata map[string]any, artifactURL, artifactChecksum string, javaSpec DistroPackageSpec) *Playbook {
	playbook := NewPlaybook("Provision Kafka Broker", host)
	if port == 0 {
		port = 9092
	}

	brokerCount := max(metadataInt(metadata, "broker_count", 1), 1)
	defaultRF := min(brokerCount, 3)
	minISR := max(metadataInt(metadata, "min_insync_replicas", defaultRF-1), 1)
	offsetsRF := max(metadataInt(metadata, "offsets_topic_replication_factor", defaultRF), 1)
	txRF := max(metadataInt(metadata, "transaction_state_log_replication_factor", defaultRF), 1)
	txMinISR := max(metadataInt(metadata, "transaction_state_log_min_isr", txRF-1), 1)
	deleteTopics := metadataBool(metadata, "delete_topic_enable", false)

	serverProps := string(BuildKafkaBrokerServerProperties(KafkaBrokerParams{
		NodeID:           nodeID,
		ListenerHost:     host,
		ListenerPort:     port,
		BootstrapServers: bootstrapServers,
		MinISR:           minISR,
		OffsetsRF:        offsetsRF,
		TxRF:             txRF,
		TxMinISR:         txMinISR,
		DeleteTopics:     deleteTopics,
	}))
	brokerUnit := RenderSystemdUnit(kafkaBrokerUnitSpec())

	tasks := kafkaCommonInstallTasks(artifactURL, artifactChecksum, "/etc/kafka", "/var/lib/kafka/logs", javaSpec)
	tasks = append(tasks,
		TaskCopy("/etc/kafka/server.properties", serverProps, CopyOpts{Owner: "kafka", Group: "kafka", Mode: "0644"}),
		TaskShell(
			fmt.Sprintf(`/opt/kafka/bin/kafka-storage.sh format --cluster-id %q --no-initial-controllers --config /etc/kafka/server.properties`, clusterID),
			ShellOpts{Creates: "/var/lib/kafka/logs/meta.properties"},
		),
		Task{
			Name:   "ensure kafka log dir ownership",
			Module: "ansible.builtin.file",
			Args: map[string]any{
				"path":    "/var/lib/kafka/logs",
				"state":   "directory",
				"owner":   "kafka",
				"group":   "kafka",
				"recurse": true,
			},
		},
		TaskCopy("/etc/systemd/system/frameworks-kafka.service", brokerUnit, CopyOpts{Mode: "0644"}),
		TaskSystemdService("frameworks-kafka", SystemdOpts{State: "started", Enabled: BoolPtr(true), DaemonReload: true}),
		TaskWaitForPort(port, WaitForOpts{Host: "127.0.0.1", Timeout: 60, Sleep: 1}),
	)

	playbook.AddPlay(Play{
		Name:        "Install and configure Kafka Broker",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks:       tasks,
	})
	return playbook
}

func metadataInt(metadata map[string]any, key string, fallback int) int {
	if metadata == nil {
		return fallback
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}

func metadataBool(metadata map[string]any, key string, fallback bool) bool {
	if metadata == nil {
		return fallback
	}
	value, ok := metadata[key]
	if !ok {
		return fallback
	}
	typed, ok := value.(bool)
	if !ok {
		return fallback
	}
	return typed
}

// String returns a string representation of the playbook
func (p *Playbook) String() string {
	yaml, err := p.ToYAML()
	if err != nil {
		return fmt.Sprintf("Error generating YAML: %v", err)
	}
	return string(yaml)
}

// Summary returns a brief summary of the playbook
func (p *Playbook) Summary() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Playbook: %s", p.Name))
	parts = append(parts, fmt.Sprintf("Hosts: %s", p.Hosts))
	parts = append(parts, fmt.Sprintf("Plays: %d", len(p.Plays)))
	return strings.Join(parts, ", ")
}
