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

const defaultApacheKafkaVersion = "3.6.0"

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

func GeneratePostgresPlaybook(host, version string, databases []string, downloadSnippet string) *Playbook {
	playbook := NewPlaybook("Provision PostgreSQL", host)
	normalizedVersion := strings.TrimSpace(version)
	switch normalizedVersion {
	case "", "latest", "stable":
		normalizedVersion = ""
	}
	managedConf := string(PostgresManagedConfBlock())
	managedHBA := string(PostgresManagedHBABlock())

	installScript := fmt.Sprintf(`set -euo pipefail

PG_SERVICE=postgresql
PGCONF=""
PGHBA=""
POSTGRES_VERSION=%q
PG_MAJOR="${POSTGRES_VERSION%%.*}"
FRAMEWORKS_PG_MANAGED_CONF=$(cat <<'FRAMEWORKS_PG_CONF_EOF'
%s
FRAMEWORKS_PG_CONF_EOF
)
FRAMEWORKS_PG_MANAGED_HBA=$(cat <<'FRAMEWORKS_PG_HBA_EOF'
%s
FRAMEWORKS_PG_HBA_EOF
)

if command -v apt-get >/dev/null 2>&1; then
  apt-get -o DPkg::Lock::Timeout=300 update
  if [ -n "$PG_MAJOR" ]; then
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y "postgresql-$PG_MAJOR" postgresql-contrib
  else
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y postgresql postgresql-contrib
  fi
  PGCONF=$(find /etc/postgresql -path '*/main/postgresql.conf' | head -n 1)
  PGHBA=$(find /etc/postgresql -path '*/main/pg_hba.conf' | head -n 1)
elif command -v dnf >/dev/null 2>&1; then
  shell=/usr/bin/nologin
  [ ! -x "$shell" ] && shell=/sbin/nologin
  [ ! -x "$shell" ] && shell=/bin/false
  getent group postgres >/dev/null || groupadd --system postgres
  id -u postgres >/dev/null 2>&1 || useradd -r -g postgres -d /var/lib/postgresql -s "$shell" postgres
  if [ -n "$POSTGRES_VERSION" ]; then
    dnf install -y gcc make readline-devel zlib-devel openssl-devel libicu-devel curl tar
    PGPREFIX="/opt/postgresql-${POSTGRES_VERSION}"
    if [ ! -x "${PGPREFIX}/bin/postgres" ]; then
__FRAMEWORKS_PG_DOWNLOAD__
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}"
      tar -xjf /tmp/postgresql.tar.bz2 -C /tmp
      cd "/tmp/postgresql-${POSTGRES_VERSION}"
      ./configure --prefix="${PGPREFIX}"
      make -j"$(nproc)"
      make install
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}" /tmp/postgresql.tar.bz2
    fi
    ln -sfn "${PGPREFIX}" /opt/postgresql
    install -d -m 0700 -o postgres -g postgres /var/lib/postgresql/data
    if [ ! -f /var/lib/postgresql/data/PG_VERSION ]; then
      su -s /bin/sh postgres -c '/opt/postgresql/bin/initdb -D /var/lib/postgresql/data'
    fi
    cat > /etc/systemd/system/postgresql.service <<'EOF'
[Unit]
Description=PostgreSQL database server
After=network.target

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/opt/postgresql/bin/postgres -D /var/lib/postgresql/data
ExecReload=/bin/kill -HUP $MAINPID
KillMode=mixed
KillSignal=SIGINT
TimeoutSec=0
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
    PGCONF=/var/lib/postgresql/data/postgresql.conf
    PGHBA=/var/lib/postgresql/data/pg_hba.conf
  else
    dnf install -y postgresql-server postgresql
    [ -f /var/lib/pgsql/data/PG_VERSION ] || postgresql-setup --initdb
    PGCONF=/var/lib/pgsql/data/postgresql.conf
    PGHBA=/var/lib/pgsql/data/pg_hba.conf
  fi
elif command -v yum >/dev/null 2>&1; then
  shell=/usr/bin/nologin
  [ ! -x "$shell" ] && shell=/sbin/nologin
  [ ! -x "$shell" ] && shell=/bin/false
  getent group postgres >/dev/null || groupadd --system postgres
  id -u postgres >/dev/null 2>&1 || useradd -r -g postgres -d /var/lib/postgresql -s "$shell" postgres
  if [ -n "$POSTGRES_VERSION" ]; then
    yum install -y gcc make readline-devel zlib-devel openssl-devel libicu-devel curl tar
    PGPREFIX="/opt/postgresql-${POSTGRES_VERSION}"
    if [ ! -x "${PGPREFIX}/bin/postgres" ]; then
__FRAMEWORKS_PG_DOWNLOAD__
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}"
      tar -xjf /tmp/postgresql.tar.bz2 -C /tmp
      cd "/tmp/postgresql-${POSTGRES_VERSION}"
      ./configure --prefix="${PGPREFIX}"
      make -j"$(nproc)"
      make install
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}" /tmp/postgresql.tar.bz2
    fi
    ln -sfn "${PGPREFIX}" /opt/postgresql
    install -d -m 0700 -o postgres -g postgres /var/lib/postgresql/data
    if [ ! -f /var/lib/postgresql/data/PG_VERSION ]; then
      su -s /bin/sh postgres -c '/opt/postgresql/bin/initdb -D /var/lib/postgresql/data'
    fi
    cat > /etc/systemd/system/postgresql.service <<'EOF'
[Unit]
Description=PostgreSQL database server
After=network.target

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/opt/postgresql/bin/postgres -D /var/lib/postgresql/data
ExecReload=/bin/kill -HUP $MAINPID
KillMode=mixed
KillSignal=SIGINT
TimeoutSec=0
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
    PGCONF=/var/lib/postgresql/data/postgresql.conf
    PGHBA=/var/lib/postgresql/data/pg_hba.conf
  else
    yum install -y postgresql-server postgresql
    [ -f /var/lib/pgsql/data/PG_VERSION ] || postgresql-setup initdb
    PGCONF=/var/lib/pgsql/data/postgresql.conf
    PGHBA=/var/lib/pgsql/data/pg_hba.conf
  fi
elif command -v pacman >/dev/null 2>&1; then
  shell=/usr/bin/nologin
  [ ! -x "$shell" ] && shell=/sbin/nologin
  [ ! -x "$shell" ] && shell=/bin/false
  getent group postgres >/dev/null || groupadd --system postgres
  id -u postgres >/dev/null 2>&1 || useradd -r -g postgres -d /var/lib/postgres -s "$shell" postgres
  if [ -n "$POSTGRES_VERSION" ]; then
    pacman -Syu --noconfirm --needed base-devel curl icu krb5 openssl readline zlib
    PGPREFIX="/opt/postgresql-${POSTGRES_VERSION}"
    if [ ! -x "${PGPREFIX}/bin/postgres" ]; then
__FRAMEWORKS_PG_DOWNLOAD__
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}"
      tar -xjf /tmp/postgresql.tar.bz2 -C /tmp
      cd "/tmp/postgresql-${POSTGRES_VERSION}"
      ./configure --prefix="${PGPREFIX}"
      make -j"$(nproc)"
      make install
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}" /tmp/postgresql.tar.bz2
    fi
    ln -sfn "${PGPREFIX}" /opt/postgresql
    install -d -m 0700 -o postgres -g postgres /var/lib/postgres/data
    if [ ! -f /var/lib/postgres/data/PG_VERSION ]; then
      su -s /bin/sh postgres -c '/opt/postgresql/bin/initdb -D /var/lib/postgres/data'
    fi
    cat > /etc/systemd/system/postgresql.service <<'EOF'
[Unit]
Description=PostgreSQL database server
After=network.target

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=/opt/postgresql/bin/postgres -D /var/lib/postgres/data
ExecReload=/bin/kill -HUP $MAINPID
KillMode=mixed
KillSignal=SIGINT
TimeoutSec=0
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
    PGCONF=/var/lib/postgres/data/postgresql.conf
    PGHBA=/var/lib/postgres/data/pg_hba.conf
  else
    pacman -Syu --noconfirm --needed postgresql
    install -d -m 0700 -o postgres -g postgres /var/lib/postgres/data
    if [ ! -f /var/lib/postgres/data/PG_VERSION ]; then
      su -s /bin/sh postgres -c 'initdb -D /var/lib/postgres/data'
    fi
    PGCONF=/var/lib/postgres/data/postgresql.conf
    PGHBA=/var/lib/postgres/data/pg_hba.conf
  fi
else
  echo "unsupported package manager" >&2
  exit 1
fi

if [ -z "$PGCONF" ] || [ -z "$PGHBA" ] || [ ! -f "$PGCONF" ] || [ ! -f "$PGHBA" ]; then
  echo "failed to locate postgresql.conf or pg_hba.conf" >&2
  exit 1
fi

sed -i '/# frameworks managed begin/,/# frameworks managed end/d' "$PGCONF"
printf '%%s' "${FRAMEWORKS_PG_MANAGED_CONF}" >> "$PGCONF"

sed -i '/# frameworks managed begin/,/# frameworks managed end/d' "$PGHBA"
printf '%%s' "${FRAMEWORKS_PG_MANAGED_HBA}" >> "$PGHBA"

systemctl enable postgresql
systemctl restart postgresql
%s
`, normalizedVersion, managedConf, managedHBA, postgresDatabaseBootstrapCommands(databases))
	installScript = strings.ReplaceAll(installScript, "__FRAMEWORKS_PG_DOWNLOAD__", downloadSnippet)

	play := Play{
		Name:        "Install and configure PostgreSQL",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []Task{
			{
				Name:   "Install and configure PostgreSQL packages and config",
				Module: "shell",
				Args: map[string]any{
					"cmd":        installScript,
					"executable": "/bin/bash",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
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

// BuildKafkaBrokerUnit returns the frameworks-kafka.service bytes.
func BuildKafkaBrokerUnit() []byte {
	return []byte(`[Unit]
Description=FrameWorks Kafka Broker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=kafka
Group=kafka
ExecStart=/opt/kafka/bin/kafka-server-start.sh /etc/kafka/server.properties
ExecStop=/opt/kafka/bin/kafka-server-stop.sh
Restart=always
RestartSec=5
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
`)
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

// BuildKafkaControllerUnit returns the frameworks-kafka-controller.service bytes.
func BuildKafkaControllerUnit() []byte {
	return []byte(`[Unit]
Description=FrameWorks Kafka Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=kafka
Group=kafka
ExecStart=/opt/kafka/bin/kafka-server-start.sh /etc/kafka/server.properties
ExecStop=/opt/kafka/bin/kafka-server-stop.sh
Restart=always
RestartSec=5
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
`)
}

// GenerateKafkaKRaftPlaybook creates an Ansible playbook for Kafka in KRaft mode (no ZooKeeper).
// downloadSnippet must be a bash fragment that fetches the kafka tarball to /tmp/kafka.tgz
// and verifies its checksum. Callers build it via the provisioner-side artifact resolver.
func GenerateKafkaKRaftPlaybook(version string, nodeID int, host string, port int, controllerPort int, controllerQuorum string, clusterID string, metadata map[string]any, downloadSnippet string) *Playbook {
	playbook := NewPlaybook("Provision Kafka", host)
	if port == 0 {
		port = 9092
	}
	if controllerPort == 0 {
		controllerPort = 9093
	}
	kafkaVersion := strings.TrimSpace(version)
	if kafkaVersion == "" {
		kafkaVersion = defaultApacheKafkaVersion
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
	brokerUnit := string(BuildKafkaBrokerUnit())

	installScript := fmt.Sprintf(`set -euo pipefail

KAFKA_VERSION="%s"
CLUSTER_ID="%s"
SERVER_PROPS_CONTENT=$(cat <<'FRAMEWORKS_KAFKA_SERVER_EOF'
%s
FRAMEWORKS_KAFKA_SERVER_EOF
)
BROKER_UNIT_CONTENT=$(cat <<'FRAMEWORKS_KAFKA_UNIT_EOF'
%s
FRAMEWORKS_KAFKA_UNIT_EOF
)

shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false
__FRAMEWORKS_INSTALL_JAVA__
getent group kafka >/dev/null || groupadd --system kafka
id -u kafka >/dev/null 2>&1 || useradd -r -g kafka -s "$shell" kafka

mkdir -p /opt /etc/kafka /var/lib/kafka/logs
if [ ! -x /opt/kafka/bin/kafka-server-start.sh ]; then
  rm -rf /opt/kafka
__FRAMEWORKS_KAFKA_DOWNLOAD__
  topdir=$(tar -tzf /tmp/kafka.tgz | head -n1 | cut -d/ -f1)
  rm -rf "/tmp/${topdir}"
  tar -xzf /tmp/kafka.tgz -C /tmp
  mv "/tmp/${topdir}" /opt/kafka
  rm -f /tmp/kafka.tgz
fi

printf '%%s' "${SERVER_PROPS_CONTENT}" > /etc/kafka/server.properties

if [ ! -f /var/lib/kafka/logs/meta.properties ]; then
  /opt/kafka/bin/kafka-storage.sh format \
    -t "${CLUSTER_ID}" \
    -c /etc/kafka/server.properties
fi

printf '%%s' "${BROKER_UNIT_CONTENT}" > /etc/systemd/system/frameworks-kafka.service

chown -R kafka:kafka /opt/kafka /etc/kafka /var/lib/kafka
systemctl daemon-reload
`, kafkaVersion, clusterID, serverProps, brokerUnit)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_INSTALL_JAVA__", EnsureCurlInstallSnippet+EnsureJavaRuntimeInstallSnippet, 1)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_KAFKA_DOWNLOAD__", downloadSnippet, 1)

	play := Play{
		Name:        "Install and configure Kafka",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []Task{
			{
				Name:   "Install Kafka runtime and configuration",
				Module: "shell",
				Args: map[string]any{
					"cmd":        installScript,
					"executable": "/bin/bash",
				},
			},
			{
				Name:   "Enable and start Kafka",
				Module: "systemd",
				Args: map[string]any{
					"name":    "frameworks-kafka",
					"enabled": true,
					"state":   "started",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// GenerateKafkaControllerPlaybook creates an Ansible playbook for a dedicated KRaft controller.
// downloadSnippet is the bash fragment that fetches /tmp/kafka.tgz and verifies its checksum.
func GenerateKafkaControllerPlaybook(version string, nodeID int, host string, controllerPort int, bootstrapServers string, clusterID string, initialControllers string, downloadSnippet string) *Playbook {
	playbook := NewPlaybook("Provision Kafka Controller", host)
	if controllerPort == 0 {
		controllerPort = 9093
	}
	kafkaVersion := strings.TrimSpace(version)
	if kafkaVersion == "" {
		kafkaVersion = defaultApacheKafkaVersion
	}

	serverProps := string(BuildKafkaControllerServerProperties(KafkaControllerParams{
		NodeID:           nodeID,
		ControllerPort:   controllerPort,
		BootstrapServers: bootstrapServers,
	}))
	ctrlUnit := string(BuildKafkaControllerUnit())

	installScript := fmt.Sprintf(`set -euo pipefail

KAFKA_VERSION="%s"
CLUSTER_ID="%s"
INITIAL_CONTROLLERS="%s"
SERVER_PROPS_CONTENT=$(cat <<'FRAMEWORKS_KAFKA_CTRL_SERVER_EOF'
%s
FRAMEWORKS_KAFKA_CTRL_SERVER_EOF
)
CTRL_UNIT_CONTENT=$(cat <<'FRAMEWORKS_KAFKA_CTRL_UNIT_EOF'
%s
FRAMEWORKS_KAFKA_CTRL_UNIT_EOF
)

shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false
__FRAMEWORKS_INSTALL_JAVA__
getent group kafka >/dev/null || groupadd --system kafka
id -u kafka >/dev/null 2>&1 || useradd -r -g kafka -s "$shell" kafka

mkdir -p /opt /etc/kafka-controller /var/lib/kafka-controller/logs
if [ ! -x /opt/kafka/bin/kafka-server-start.sh ]; then
  rm -rf /opt/kafka
__FRAMEWORKS_KAFKA_DOWNLOAD__
  topdir=$(tar -tzf /tmp/kafka.tgz | head -n1 | cut -d/ -f1)
  rm -rf "/tmp/${topdir}"
  tar -xzf /tmp/kafka.tgz -C /tmp
  mv "/tmp/${topdir}" /opt/kafka
  rm -f /tmp/kafka.tgz
fi

printf '%%s' "${SERVER_PROPS_CONTENT}" > /etc/kafka-controller/server.properties

if [ ! -f /var/lib/kafka-controller/logs/meta.properties ]; then
  /opt/kafka/bin/kafka-storage.sh format \
    --cluster-id "${CLUSTER_ID}" \
    --initial-controllers "${INITIAL_CONTROLLERS}" \
    --config /etc/kafka-controller/server.properties
fi

printf '%%s' "${CTRL_UNIT_CONTENT}" > /etc/systemd/system/frameworks-kafka-controller.service

chown -R kafka:kafka /opt/kafka /etc/kafka-controller /var/lib/kafka-controller
systemctl daemon-reload
`, kafkaVersion, clusterID, initialControllers, serverProps, ctrlUnit)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_INSTALL_JAVA__", EnsureCurlInstallSnippet+EnsureJavaRuntimeInstallSnippet, 1)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_KAFKA_DOWNLOAD__", downloadSnippet, 1)

	play := Play{
		Name:        "Install and configure Kafka Controller",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []Task{
			{
				Name:   "Install Kafka controller runtime and configuration",
				Module: "shell",
				Args: map[string]any{
					"cmd":        installScript,
					"executable": "/bin/bash",
				},
			},
			{
				Name:   "Enable and start Kafka Controller",
				Module: "systemd",
				Args: map[string]any{
					"name":    "frameworks-kafka-controller",
					"enabled": true,
					"state":   "started",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// GenerateKafkaBrokerPlaybook creates an Ansible playbook for a broker-only Kafka node (dedicated controller mode).
// downloadSnippet is the bash fragment that fetches /tmp/kafka.tgz and verifies its checksum.
func GenerateKafkaBrokerPlaybook(version string, nodeID int, host string, port int, bootstrapServers string, clusterID string, metadata map[string]any, downloadSnippet string) *Playbook {
	playbook := NewPlaybook("Provision Kafka Broker", host)
	if port == 0 {
		port = 9092
	}
	kafkaVersion := strings.TrimSpace(version)
	if kafkaVersion == "" {
		kafkaVersion = defaultApacheKafkaVersion
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
	brokerUnit := string(BuildKafkaBrokerUnit())

	installScript := fmt.Sprintf(`set -euo pipefail

KAFKA_VERSION="%s"
CLUSTER_ID="%s"
SERVER_PROPS_CONTENT=$(cat <<'FRAMEWORKS_KAFKA_BROKER_SERVER_EOF'
%s
FRAMEWORKS_KAFKA_BROKER_SERVER_EOF
)
BROKER_UNIT_CONTENT=$(cat <<'FRAMEWORKS_KAFKA_BROKER_UNIT_EOF'
%s
FRAMEWORKS_KAFKA_BROKER_UNIT_EOF
)

shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false
__FRAMEWORKS_INSTALL_JAVA__
getent group kafka >/dev/null || groupadd --system kafka
id -u kafka >/dev/null 2>&1 || useradd -r -g kafka -s "$shell" kafka

mkdir -p /opt /etc/kafka /var/lib/kafka/logs
if [ ! -x /opt/kafka/bin/kafka-server-start.sh ]; then
  rm -rf /opt/kafka
__FRAMEWORKS_KAFKA_DOWNLOAD__
  topdir=$(tar -tzf /tmp/kafka.tgz | head -n1 | cut -d/ -f1)
  rm -rf "/tmp/${topdir}"
  tar -xzf /tmp/kafka.tgz -C /tmp
  mv "/tmp/${topdir}" /opt/kafka
  rm -f /tmp/kafka.tgz
fi

printf '%%s' "${SERVER_PROPS_CONTENT}" > /etc/kafka/server.properties

if [ ! -f /var/lib/kafka/logs/meta.properties ]; then
  /opt/kafka/bin/kafka-storage.sh format \
    --cluster-id "${CLUSTER_ID}" \
    --no-initial-controllers \
    --config /etc/kafka/server.properties
fi

printf '%%s' "${BROKER_UNIT_CONTENT}" > /etc/systemd/system/frameworks-kafka.service

chown -R kafka:kafka /opt/kafka /etc/kafka /var/lib/kafka
systemctl daemon-reload
`, kafkaVersion, clusterID, serverProps, brokerUnit)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_INSTALL_JAVA__", EnsureCurlInstallSnippet+EnsureJavaRuntimeInstallSnippet, 1)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_KAFKA_DOWNLOAD__", downloadSnippet, 1)

	play := Play{
		Name:        "Install and configure Kafka Broker",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []Task{
			{
				Name:   "Install Kafka broker runtime and configuration",
				Module: "shell",
				Args: map[string]any{
					"cmd":        installScript,
					"executable": "/bin/bash",
				},
			},
			{
				Name:   "Enable and start Kafka Broker",
				Module: "systemd",
				Args: map[string]any{
					"name":    "frameworks-kafka",
					"enabled": true,
					"state":   "started",
				},
			},
		},
	}

	playbook.AddPlay(play)
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

func postgresDatabaseBootstrapCommands(databases []string) string {
	if len(databases) == 0 {
		return ""
	}

	var commands []string
	for _, database := range databases {
		name := strings.TrimSpace(database)
		if name == "" {
			continue
		}
		quoted := strings.ReplaceAll(name, `'`, `''`)
		commands = append(commands,
			fmt.Sprintf("su -s /bin/sh postgres -c \"psql -tAc \\\"SELECT 1 FROM pg_database WHERE datname='%s'\\\" | grep -q 1 || createdb %s\"", quoted, name),
		)
	}
	return strings.Join(commands, "\n")
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
