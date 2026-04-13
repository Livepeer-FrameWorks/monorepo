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
	plays := make([]map[string]interface{}, 0, len(p.Plays))

	for _, play := range p.Plays {
		playMap := map[string]interface{}{
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
func convertTasks(tasks []Task) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tasks))

	for _, task := range tasks {
		taskMap := map[string]interface{}{
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
func convertRoles(roles []Role) []interface{} {
	result := make([]interface{}, 0, len(roles))

	for _, role := range roles {
		if len(role.Vars) > 0 {
			result = append(result, map[string]interface{}{
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
func convertHandlers(handlers []Handler) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(handlers))

	for _, handler := range handlers {
		handlerMap := map[string]interface{}{
			"name":         handler.Name,
			handler.Module: handler.Args,
		}
		result = append(result, handlerMap)
	}

	return result
}

const defaultApacheKafkaVersion = "3.6.0"

// GeneratePostgresPlaybook creates an Ansible playbook for PostgreSQL.
func GeneratePostgresPlaybook(host, version string, databases []string) *Playbook {
	playbook := NewPlaybook("Provision PostgreSQL", host)
	normalizedVersion := strings.TrimSpace(version)
	switch normalizedVersion {
	case "", "latest", "stable":
		normalizedVersion = ""
	}

	installScript := fmt.Sprintf(`set -euo pipefail

PG_SERVICE=postgresql
PGCONF=""
PGHBA=""
POSTGRES_VERSION=%q
PG_MAJOR="${POSTGRES_VERSION%%.*}"

checksum_value() {
  awk 'NF { print $1; exit }' "$1"
}

verify_checksum() {
  local algorithm="$1" file="$2" checksum_file="$3" expected actual
  expected="$(checksum_value "$checksum_file")"
  [ -n "$expected" ] || { echo "missing checksum in $checksum_file" >&2; exit 1; }
  case "$algorithm" in
    sha256)
      if command -v sha256sum >/dev/null 2>&1; then
        actual="$(sha256sum "$file" | awk '{print $1}')"
      elif command -v shasum >/dev/null 2>&1; then
        actual="$(shasum -a 256 "$file" | awk '{print $1}')"
      else
        actual="$(openssl dgst -sha256 "$file" | awk '{print $NF}')"
      fi
      ;;
    *)
      echo "unsupported checksum algorithm: $algorithm" >&2
      exit 1
      ;;
  esac
  [ "$actual" = "$expected" ] || {
    echo "checksum mismatch for $file" >&2
    echo "expected: $expected" >&2
    echo "actual:   $actual" >&2
    exit 1
  }
}

if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  if [ -n "$PG_MAJOR" ]; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y "postgresql-$PG_MAJOR" postgresql-contrib
  else
    DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql postgresql-contrib
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
      curl -fsSL -o /tmp/postgresql.tar.bz2 "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2"
      curl -fsSL -o /tmp/postgresql.tar.bz2.sha256 "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2.sha256"
      verify_checksum sha256 /tmp/postgresql.tar.bz2 /tmp/postgresql.tar.bz2.sha256
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}"
      tar -xjf /tmp/postgresql.tar.bz2 -C /tmp
      cd "/tmp/postgresql-${POSTGRES_VERSION}"
      ./configure --prefix="${PGPREFIX}"
      make -j"$(nproc)"
      make install
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}" /tmp/postgresql.tar.bz2 /tmp/postgresql.tar.bz2.sha256
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
      curl -fsSL -o /tmp/postgresql.tar.bz2 "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2"
      curl -fsSL -o /tmp/postgresql.tar.bz2.sha256 "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2.sha256"
      verify_checksum sha256 /tmp/postgresql.tar.bz2 /tmp/postgresql.tar.bz2.sha256
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}"
      tar -xjf /tmp/postgresql.tar.bz2 -C /tmp
      cd "/tmp/postgresql-${POSTGRES_VERSION}"
      ./configure --prefix="${PGPREFIX}"
      make -j"$(nproc)"
      make install
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}" /tmp/postgresql.tar.bz2 /tmp/postgresql.tar.bz2.sha256
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
      curl -fsSL -o /tmp/postgresql.tar.bz2 "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2"
      curl -fsSL -o /tmp/postgresql.tar.bz2.sha256 "https://ftp.postgresql.org/pub/source/v${POSTGRES_VERSION}/postgresql-${POSTGRES_VERSION}.tar.bz2.sha256"
      verify_checksum sha256 /tmp/postgresql.tar.bz2 /tmp/postgresql.tar.bz2.sha256
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}"
      tar -xjf /tmp/postgresql.tar.bz2 -C /tmp
      cd "/tmp/postgresql-${POSTGRES_VERSION}"
      ./configure --prefix="${PGPREFIX}"
      make -j"$(nproc)"
      make install
      rm -rf "/tmp/postgresql-${POSTGRES_VERSION}" /tmp/postgresql.tar.bz2 /tmp/postgresql.tar.bz2.sha256
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
cat >> "$PGCONF" <<'EOF'
# frameworks managed begin
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
EOF

sed -i '/# frameworks managed begin/,/# frameworks managed end/d' "$PGHBA"
cat >> "$PGHBA" <<'EOF'
# frameworks managed begin
host all all 127.0.0.1/32 scram-sha-256
host all all ::1/128 scram-sha-256
host all all 0.0.0.0/0 scram-sha-256
host all all ::/0 scram-sha-256
# frameworks managed end
EOF

systemctl enable postgresql
systemctl restart postgresql
%s
`, normalizedVersion, postgresDatabaseBootstrapCommands(databases))

	play := Play{
		Name:        "Install and configure PostgreSQL",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []Task{
			{
				Name:   "Install and configure PostgreSQL packages and config",
				Module: "shell",
				Args: map[string]interface{}{
					"cmd":        installScript,
					"executable": "/bin/bash",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// GenerateKafkaKRaftPlaybook creates an Ansible playbook for Kafka in KRaft mode (no ZooKeeper).
func GenerateKafkaKRaftPlaybook(version string, nodeID int, host string, port int, controllerPort int, controllerQuorum string, clusterID string, metadata map[string]interface{}) *Playbook {
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

	brokerCount := metadataInt(metadata, "broker_count", 1)
	if brokerCount < 1 {
		brokerCount = 1
	}
	defaultRF := brokerCount
	if defaultRF > 3 {
		defaultRF = 3
	}
	minISR := metadataInt(metadata, "min_insync_replicas", defaultRF-1)
	if minISR < 1 {
		minISR = 1
	}
	offsetsRF := metadataInt(metadata, "offsets_topic_replication_factor", defaultRF)
	if offsetsRF < 1 {
		offsetsRF = 1
	}
	txRF := metadataInt(metadata, "transaction_state_log_replication_factor", defaultRF)
	if txRF < 1 {
		txRF = 1
	}
	txMinISR := metadataInt(metadata, "transaction_state_log_min_isr", txRF-1)
	if txMinISR < 1 {
		txMinISR = 1
	}
	deleteTopics := metadataBool(metadata, "delete_topic_enable", false)

	installScript := fmt.Sprintf(`set -euo pipefail

KAFKA_VERSION="%s"
NODE_ID="%d"
LISTENER_HOST="%s"
LISTENER_PORT="%d"
CONTROLLER_PORT="%d"
CONTROLLER_QUORUM_VOTERS="%s"
CLUSTER_ID="%s"
MIN_INSYNC_REPLICAS="%d"
OFFSETS_RF="%d"
TX_RF="%d"
TX_MIN_ISR="%d"
DELETE_TOPICS="%t"

checksum_value() {
  awk 'NF { print $1; exit }' "$1"
}

verify_checksum() {
  local algorithm="$1" file="$2" checksum_file="$3" expected actual
  expected="$(checksum_value "$checksum_file")"
  [ -n "$expected" ] || { echo "missing checksum in $checksum_file" >&2; exit 1; }
  case "$algorithm" in
    sha512)
      if command -v sha512sum >/dev/null 2>&1; then
        actual="$(sha512sum "$file" | awk '{print $1}')"
      elif command -v shasum >/dev/null 2>&1; then
        actual="$(shasum -a 512 "$file" | awk '{print $1}')"
      else
        actual="$(openssl dgst -sha512 "$file" | awk '{print $NF}')"
      fi
      ;;
    *)
      echo "unsupported checksum algorithm: $algorithm" >&2
      exit 1
      ;;
  esac
  [ "$actual" = "$expected" ] || {
    echo "checksum mismatch for $file" >&2
    echo "expected: $expected" >&2
    echo "actual:   $actual" >&2
    exit 1
  }
}

shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false

if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates default-jre-headless
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y curl java-17-openjdk-headless
elif command -v yum >/dev/null 2>&1; then
  yum install -y curl java-17-openjdk-headless
elif command -v pacman >/dev/null 2>&1; then
  pacman -Syu --noconfirm --needed curl jre-openjdk-headless
else
  echo "unsupported package manager" >&2
  exit 1
fi

getent group kafka >/dev/null || groupadd --system kafka
id -u kafka >/dev/null 2>&1 || useradd -r -g kafka -s "$shell" kafka

mkdir -p /opt /etc/kafka /var/lib/kafka/logs
if [ ! -x /opt/kafka/bin/kafka-server-start.sh ]; then
  rm -rf /opt/kafka /tmp/kafka_2.13-${KAFKA_VERSION}
  curl -fsSL -o /tmp/kafka.tgz "https://downloads.apache.org/kafka/${KAFKA_VERSION}/kafka_2.13-${KAFKA_VERSION}.tgz"
  curl -fsSL -o /tmp/kafka.tgz.sha512 "https://downloads.apache.org/kafka/${KAFKA_VERSION}/kafka_2.13-${KAFKA_VERSION}.tgz.sha512"
  verify_checksum sha512 /tmp/kafka.tgz /tmp/kafka.tgz.sha512
  tar -xzf /tmp/kafka.tgz -C /tmp
  mv /tmp/kafka_2.13-${KAFKA_VERSION} /opt/kafka
  rm -f /tmp/kafka.tgz /tmp/kafka.tgz.sha512
fi

cat > /etc/kafka/server.properties <<EOF
node.id=${NODE_ID}
process.roles=broker,controller
controller.quorum.voters=${CONTROLLER_QUORUM_VOTERS}
controller.listener.names=CONTROLLER
listeners=PLAINTEXT://0.0.0.0:${LISTENER_PORT},CONTROLLER://0.0.0.0:${CONTROLLER_PORT}
advertised.listeners=PLAINTEXT://${LISTENER_HOST}:${LISTENER_PORT}
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
min.insync.replicas=${MIN_INSYNC_REPLICAS}
offsets.topic.replication.factor=${OFFSETS_RF}
transaction.state.log.replication.factor=${TX_RF}
transaction.state.log.min.isr=${TX_MIN_ISR}
log.retention.hours=168
group.initial.rebalance.delay.ms=0
auto.create.topics.enable=false
delete.topic.enable=${DELETE_TOPICS}
EOF

if [ ! -f /var/lib/kafka/logs/meta.properties ]; then
  /opt/kafka/bin/kafka-storage.sh format \
    -t "${CLUSTER_ID}" \
    -c /etc/kafka/server.properties
fi

cat > /etc/systemd/system/frameworks-kafka.service <<'EOF'
[Unit]
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
EOF

chown -R kafka:kafka /opt/kafka /etc/kafka /var/lib/kafka
systemctl daemon-reload
`, kafkaVersion, nodeID, host, port, controllerPort, controllerQuorum, clusterID, minISR, offsetsRF, txRF, txMinISR, deleteTopics)

	play := Play{
		Name:        "Install and configure Kafka",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Tasks: []Task{
			{
				Name:   "Install Kafka runtime and configuration",
				Module: "shell",
				Args: map[string]interface{}{
					"cmd":        installScript,
					"executable": "/bin/bash",
				},
			},
			{
				Name:   "Enable and start Kafka",
				Module: "systemd",
				Args: map[string]interface{}{
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

func metadataInt(metadata map[string]interface{}, key string, fallback int) int {
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

func metadataBool(metadata map[string]interface{}, key string, fallback bool) bool {
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
