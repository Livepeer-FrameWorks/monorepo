# FrameWorks Production Deployment Guide

> **Important: Platform Architecture Guide**  
> This guide provides **detailed bare-metal deployment** instructions showing the platform architecture at its lowest level.  
> Understanding these fundamentals is crucial for production deployments, troubleshooting, and performance optimization.

## Choose Your Deployment Method

| Method | Use Case | Complexity | Documentation |
|--------|----------|------------|---------------|
| **[FrameWorks CLI](../cli/)** | **Edge node automation** | Low | Docs: [CLI](../cli/README.md) |
| **[Docker Compose](./provisioning/docker-compose/production-overrides.md)** | **Containerized deployment** | Medium | Docs: [Guides](./provisioning/) |
| **Bare Metal** (This guide) | Platform fundamentals, OS tuning, custom configs | High | See below |
| **Terraform + Ansible** | Infrastructure as Code | Medium | Planned |
| **Kubernetes** | Cloud-native scaling | High | Planned |

## Documentation Structure

For easier deployment, check out our new guides:

- **[Provisioning Overview](./provisioning/)** - Choose your deployment path
- **[Docker Compose Guides](./provisioning/docker-compose/)** - Production-ready containerized deployment
  - [External Services Setup](./provisioning/docker-compose/external-services.md) - CloudFlare, SMTP, Stripe
  - [Production Overrides](./provisioning/docker-compose/production-overrides.md) - Resource limits, security
  - [Backup & Restore](./operations/backup-restore.md) - Data protection procedures
  - [Troubleshooting](./operations/troubleshooting.md) - Common issues and solutions

---

> **Caution: Bare‑metal complexity**  
> The bare-metal deployment below is a complex multi-tier setup with private networking, distributed databases, and real-time streaming infrastructure.
> This guide is helpful for those experimenting with the stack or needing greater insight into the components.
> **Consider Docker Compose first** unless you specifically need bare-metal deployment.

## Overview

This guide demonstrates a **production-ready FrameWorks deployment** based on our current MVP setup. It covers:

- **Multi-tier architecture** (Central/Regional/Edge)
- **Private mesh networking** (WireGuard)
- **Distributed database** (YugabyteDB cluster)
- **Event streaming** (Kafka cluster)
- **Load balancing & SSL** (Nginx + Certbot + Cloudflare)
- **System tuning** for high-performance streaming
- **DNS & domain management**

## Architecture Overview

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   CENTRAL       │    │   REGIONAL      │    │   EDGE          │
│                 │    │                 │    │                 │
├─────────────────┤    ├─────────────────┤    ├─────────────────┤
│ • Bridge        │    │ • Sales Website │    │ • MistServer    │
│ • Commodore     │    │ • WebApp        │    │ • Helmsman      │
│ • Quartermaster │    │ • Signalman     │    │ • Nginx         │
│ • Periscope     │    │ • Decklog       │    │                 │
│ • Purser        │    │ • Kafka         │    │                 │
│ • Foghorn       │    │ • Nginx         │    │                 │
│ • Forms         │    │ • Nginx         │    │                 │
│ • Forum         │    │ • Parlor 🚧     │    │                 │
│ • Metrics       │    │                 │    │                 │
│ • Lookout 🚧    │    │                 │    │                 │
│ • Deckhand 🚧   │    │                 │    │                 │
│ • Kafka         │    │                 │    │                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │                       │
         └───────VPN─MESH────────┼──────VPN─MESH─────────┘
                                 │
                        ┌─────────────────┐
                        │   DATABASE      │
                        │                 │
                        ├─────────────────┤
                        │ • YugabyteDB    │
                        │   (3-node       │
                        │    cluster)     │
                        └─────────────────┘
```

## Domain & DNS Strategy

### Domain Structure
```
frameworks.network (primary)
├── app.frameworks.network          → Regional WebApp
├── api.frameworks.network          → Regional GraphQL Gateway (Bridge)
├── commodore.frameworks.network    → Central Control API
├── quartermaster.frameworks.network → Central Tenant API
├── periscope.frameworks.network    → Central Analytics API
├── purser.frameworks.network       → Central Billing API
├── foghorn.frameworks.network      → Central Load Balancer
├── forms.frameworks.network        → Central Forms API
├── forum.frameworks.network        → Central Forum (Discourse)
├── stats.frameworks.network        → Central Metrics (Prometheus/Grafana)
├── lookout.frameworks.network      → Central Incident API 🚧
├── messenger.frameworks.network    → Regional Chat API 🚧
├── deckhand.frameworks.network     → Central Ticket API 🚧
├── signalman.frameworks.network    → Regional WebSocket API
├── decklog.frameworks.network      → Regional Event API
├── edge.frameworks.network         → Closest Edge (Media)
└── db.frameworks.network           → Database (private)
    kafka.frameworks.network        → Kafka (private)
```

### DNS Configuration
- **Registrar**: <Your favorite registrar>
- **DNS Provider**: Cloudflare
- **Load Balancing**: Cloudflare pools with health checks
- **SSL**: Cloudflare + Let's Encrypt (Certbot)

## Step 1: WireGuard Mesh Network

### Network Topology
```
10.10.0.0/24 - Private mesh network

Central:
├── 10.10.0.1   frameworks-central-eu-1

Regional:
├── 10.10.0.2   frameworks-regional-eu-1
├── 10.10.0.3   frameworks-regional-us-1

Database:
├── 10.10.0.11  frameworks-yuga-eu-1
├── 10.10.0.12  frameworks-yuga-eu-2
├── 10.10.0.13  frameworks-yuga-eu-3
├── 10.10.0.14  frameworks-yuga-us-1
└── 10.10.0.15  frameworks-yuga-us-2
```

### Installation

**Arch Linux:**
```bash
sudo pacman -S wireguard-tools
```

**Ubuntu/Debian:**
```bash
sudo apt update && sudo apt install wireguard
```

### Key Generation
```bash
mkdir /root/wireguard-keys && cd /root/wireguard-keys
wg genkey | tee private.key | wg pubkey > public.key
chmod 600 private.key
```

### Configuration Example (`/etc/wireguard/wg0.conf`)

**Central Node (10.10.0.1):**
```ini
[Interface]
PrivateKey = <central-private-key>
Address = 10.10.0.1/24
ListenPort = 51820

[Peer]
# Regional EU
PublicKey = <regional-eu-public-key>
AllowedIPs = 10.10.0.2/32
Endpoint = <peer public IP>:51820
PersistentKeepalive = 25

[Peer]
# Regional US
PublicKey = <regional-us-public-key>
AllowedIPs = 10.10.0.3/32
Endpoint = <Regional US public IP>:51820
PersistentKeepalive = 25

[Peer]
# Database EU-1
PublicKey = <yuga-eu-1-public-key>
AllowedIPs = 10.10.0.11/32
Endpoint = <Database EU-1 public IP>:51820
PersistentKeepalive = 25

# ... repeat for all nodes
```

### Activation
```bash
wg-quick up wg0
systemctl enable wg-quick@wg0
```

### Internal DNS Resolution (`/etc/hosts`)
```
# Database cluster
10.10.0.11   yuga-eu-1.cluster.local db.frameworks.network
10.10.0.12   yuga-eu-2.cluster.local db.frameworks.network
10.10.0.13   yuga-eu-3.cluster.local db.frameworks.network
10.10.0.14   yuga-us-1.cluster.local db.frameworks.network
10.10.0.15   yuga-us-2.cluster.local db.frameworks.network

# Kafka cluster  
10.10.0.1    kafka-central.cluster.local kafka.frameworks.network
10.10.0.2    kafka-eu.cluster.local
10.10.0.3    kafka-us.cluster.local
```

## Step 2: Database Cluster

### YugabyteDB (State & Configuration)

#### System Preparation
```bash
# Create yugabyte user
useradd --system --home /opt/yugabyte --shell /usr/sbin/nologin yugabyte
mkdir -p /var/lib/yugabyte
chown -R yugabyte:yugabyte /var/lib/yugabyte

# System tuning for database performance
echo 'vm.swappiness=1' >> /etc/sysctl.conf
echo 'vm.max_map_count=262144' >> /etc/sysctl.conf
echo 'net.core.somaxconn=1024' >> /etc/sysctl.conf
sysctl -p
```

#### Installation
```bash
mkdir -p /opt/yugabyte && cd /opt/yugabyte
wget https://software.yugabyte.com/releases/2025.1.0.0/yugabyte-2025.1.0.0-b168-linux-x86_64.tar.gz
tar xzf yugabyte-2025.1.0.0-b168-linux-x86_64.tar.gz --strip-components=1
rm yugabyte-2025.1.0.0-b168-linux-x86_64.tar.gz
./bin/post_install.sh
chown -R yugabyte:yugabyte /opt/yugabyte
```

#### Systemd Services

**`/etc/systemd/system/yb-master.service`:**
```ini
[Unit]
Description=YugabyteDB Master
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=yugabyte
Group=yugabyte
ExecStart=/opt/yugabyte/bin/yb-master \
  --fs_data_dirs=/var/lib/yugabyte \
  --master_addresses=10.10.0.11:7100,10.10.0.12:7100,10.10.0.13:7100,10.10.0.14:7100,10.10.0.15:7100 \
  --rpc_bind_addresses=10.10.0.XX:7100 \
  --webserver_interface=10.10.0.XX \
  --webserver_port=7000 \
  --replication_factor=3 \
  --placement_cloud=hetzner \
  --placement_region=eu-central \
  --placement_zone=fsn1
Restart=always
RestartSec=5
LimitNOFILE=1048576
LimitCORE=infinity

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/yb-tserver.service`:**
```ini
[Unit]
Description=YugabyteDB TServer
After=network-online.target yb-master.service
Wants=network-online.target

[Service]
Type=simple
User=yugabyte
Group=yugabyte
ExecStart=/opt/yugabyte/bin/yb-tserver \
  --fs_data_dirs=/var/lib/yugabyte \
  --tserver_master_addrs=10.10.0.11:7100,10.10.0.12:7100,10.10.0.13:7100,10.10.0.14:7100,10.10.0.15:7101 \
  --rpc_bind_addresses=10.10.0.XX:7101 \
  --webserver_interface=10.10.0.XX \
  --webserver_port=7001 \
  --placement_cloud=hetzner \
  --placement_region=eu-central \
  --placement_zone=fsn1
Restart=always
RestartSec=5
LimitNOFILE=1048576
LimitCORE=infinity

[Install]
WantedBy=multi-user.target
```

#### Activation
```bash
systemctl daemon-reload
systemctl enable yb-master yb-tserver
systemctl start yb-master yb-tserver
```

### ClickHouse (Time-Series Analytics)

#### System Preparation
```bash
# Create clickhouse user
useradd --system --home /opt/clickhouse --shell /usr/sbin/nologin clickhouse
mkdir -p /var/lib/clickhouse
mkdir -p /var/lib/clickhouse/coordination/log
mkdir -p /var/lib/clickhouse/coordination/snapshots
chown -R clickhouse:clickhouse /var/lib/clickhouse

# System tuning for analytics performance
echo 'vm.swappiness=1' >> /etc/sysctl.conf
echo 'vm.max_map_count=262144' >> /etc/sysctl.conf
echo 'net.core.somaxconn=1024' >> /etc/sysctl.conf
sysctl -p

```

#### Installation
```bash
# Install ClickHouse server + client (Debian/Ubuntu)
apt-get update && apt-get install -y apt-transport-https ca-certificates gnupg

GNUPGHOME=$(mktemp -d)
gpg --homedir "$GNUPGHOME" --keyserver keyserver.ubuntu.com --recv-keys E0C56BD4
gpg --homedir "$GNUPGHOME" --export E0C56BD4 | tee /etc/apt/trusted.gpg.d/clickhouse.asc > /dev/null
rm -r "$GNUPGHOME"

echo "deb https://packages.clickhouse.com/deb stable main" > /etc/apt/sources.list.d/clickhouse.list
apt-get update
apt-get install -y clickhouse-server clickhouse-client

# Prepare config directories
mkdir -p /etc/clickhouse-server/config.d
mkdir -p /etc/clickhouse-server/users.d
chown -R clickhouse:clickhouse /etc/clickhouse-server
```

#### Configuration (`/etc/clickhouse-server/config.d/keeper.xml`)

> Customize server_id per node (1, 2, 3). Hostnames must match your internal mesh.

```xml
<yandex>
    <keeper_server>
        <tcp_port>9181</tcp_port>
        <server_id>3</server_id> <!-- CHANGE PER NODE -->
        <log_storage_path>/var/lib/clickhouse/coordination/log</log_storage_path>
        <snapshot_storage_path>/var/lib/clickhouse/coordination/snapshots</snapshot_storage_path>
        <raft_configuration>
            <server>
                <id>1</id>
                <hostname>db-1.cluster.local</hostname>
                <port>9181</port>
            </server>
            <server>
                <id>2</id>
                <hostname>db-2.cluster.local</hostname>
                <port>9181</port>
            </server>
            <server>
                <id>3</id>
                <hostname>db-3.cluster.local</hostname>
                <port>9181</port>
            </server>
        </raft_configuration>
    </keeper_server>
</yandex>
```

#### Configuration (`/etc/clickhouse-server/config.d/listen.xml`)
```xml
<yandex>
    <listen_host>0.0.0.0</listen_host>
    <http_port>8123</http_port>
    <tcp_port>9000</tcp_port>
    <interserver_http_port>9009</interserver_http_port>
</yandex>
```

#### User Setup (`/etc/clickhouse-server/config.d/clickhouse_remote_servers.xml`)
```xml
<yandex>
    <remote_servers>
        <frameworks_analytics_cluster>
            <shard>
                <replica>
                    <host>db-1.cluster.local</host>
                    <port>9000</port>
                </replica>
                <replica>
                    <host>db-2.cluster.local</host>
                    <port>9000</port>
                </replica>
                <replica>
                    <host>db-3.cluster.local</host>
                    <port>9000</port>
                </replica>
            </shard>
        </frameworks_analytics_cluster>
    </remote_servers>
</yandex>
```

#### (`/etc/clickhouse-server/config.d/clickhouse_compression.xml`)
```xml
<yandex>
    <compression>
        <case>
            <min_part_size>10000000000</min_part_size>
            <min_part_size_ratio>0.01</min_part_size_ratio>
            <method>lz4</method>
        </case>
    </compression>
</yandex>
```

#### (`/etc/clickhouse-server/config.d/networks.xml`)
```xml
<yandex>
    <networks>
        <ip>::/0</ip> <!-- Secure this in mesh/firewall! -->
    </networks>
</yandex>
```

#### (`/etc/clickhouse-server/users.d/frameworks-user.xml`)
```xml
<yandex>
    <users>
        <frameworks>
            <password_sha256_hex>0abc6d42d4776c5479e1e89943f678c9692b6b803fe7d56eed35813d3276c728</password_sha256_hex>
            <networks incl="networks" />
            <profile>default</profile>
            <quota>default</quota>
            <access_management>1</access_management>
        </frameworks>
    </users>
</yandex>
```

#### Activation
```bash
systemctl daemon-reexec
systemctl enable clickhouse-server
systemctl restart clickhouse-server
```

#### Schema Initialization
```bash
# Initialize schema
clickhouse-client --user frameworks --password frameworks_dev --query="$(cat database/init_clickhouse_periscope.sql)"
```

#### Checks
```bash
clickhouse-client --user frameworks --password frameworks_dev --query="SELECT * FROM system.clusters"
clickhouse-client --user frameworks --password frameworks_dev --query="SELECT * FROM system.replicas"
```


## Step 3: Kafka Cluster

### System Preparation
```bash
# Create kafka user
useradd --system --home /opt/kafka --shell /usr/sbin/nologin kafka
mkdir -p /var/lib/kafka-logs
chown -R kafka:kafka /var/lib/kafka-logs

# Install OpenJDK
# Ubuntu/Debian:
apt install openjdk-17-jre-headless
# Arch:
pacman -S jdk-openjdk
```

### Installation
```bash
mkdir -p /opt/kafka && cd /opt/kafka
wget https://dlcdn.apache.org/kafka/3.9.1/kafka_2.13-3.9.1.tgz
tar -xzf kafka_2.13-3.9.1.tgz --strip-components=1
rm kafka_2.13-3.9.1.tgz
chown -R kafka:kafka /opt/kafka
```

### Configuration (`/opt/kafka/config/server.properties`)
```properties
# Broker configuration
broker.id=0  # Use 1, 2 for other nodes
listeners=PLAINTEXT://10.10.0.X:9092
advertised.listeners=PLAINTEXT://10.10.0.X:9092
log.dirs=/var/lib/kafka-logs # optional

# Zookeeper
zookeeper.connect=10.10.0.1:2181,10.10.0.2:2181,10.10.0.3:2181

# Performance tuning
num.network.threads=8
num.io.threads=16
socket.send.buffer.bytes=102400
socket.receive.buffer.bytes=102400
socket.request.max.bytes=104857600

# Log retention
log.retention.hours=168
log.segment.bytes=1073741824
log.retention.check.interval.ms=300000

# Replication
default.replication.factor=3
min.insync.replicas=2
```

### Systemd Services

**`/etc/systemd/system/zookeeper.service`:**
```ini
[Unit]
Description=Apache Zookeeper Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=kafka
Group=kafka
ExecStart=/opt/kafka/bin/zookeeper-server-start.sh /opt/kafka/config/zookeeper.properties
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/kafka.service`:**
```ini
[Unit]
Description=Apache Kafka Broker
After=network-online.target zookeeper.service
Wants=network-online.target

[Service]
Type=simple
User=kafka
Group=kafka
Environment=KAFKA_HEAP_OPTS="-Xmx2G -Xms2G"
ExecStart=/opt/kafka/bin/kafka-server-start.sh /opt/kafka/config/server.properties
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

### Activation
```bash
systemctl daemon-reload
systemctl enable zookeeper kafka
systemctl start zookeeper kafka
```

## Step 4: System Tuning

### Network Performance Tuning
```bash
# Add to /etc/sysctl.conf
cat >> /etc/sysctl.conf << EOF
# Network performance tuning for streaming
net.core.rmem_max=12582912
net.core.rmem_default=6291456
net.core.wmem_max=12582912
net.core.wmem_default=6291456
net.ipv4.udp_mem=262144 327680 393216
net.ipv4.udp_rmem_min=262144
net.ipv4.udp_wmem_min=262144
net.core.netdev_max_backlog=5000
net.ipv4.tcp_rmem=4096 65536 12582912
net.ipv4.tcp_wmem=4096 65536 12582912
net.ipv4.tcp_congestion_control=bbr
EOF

sysctl -p
```

### File Limits
```bash
# Add to /etc/security/limits.conf
cat >> /etc/security/limits.conf << EOF
* soft nofile 65536
* hard nofile 65536
* soft nproc 32768
* hard nproc 32768
EOF
```

## Step 5: Nginx with Geographic Routing

### Geographic IP Detection Options

FrameWorks supports multiple approaches for geographic IP detection used by Foghorn for intelligent load balancing:

1. **CloudFlare Headers** (Recommended for production)
   - Uses `CF-IPCountry`, `CF-Connecting-IP`, `CF-IPLatitude`, `CF-IPLongitude` headers
   - No additional setup required when using CloudFlare proxy
   - Most accurate and requires no maintenance

2. **Standard GeoIP2 Module** (Alternative)
   - Uses MaxMind GeoLite2 databases with nginx-module-geoip2 
   - Available as package on most distributions
   - Requires periodic database updates

3. **Custom Nginx Build** (Advanced users only)
   - Only needed for specific customizations or older distributions
   - Most users should use standard packages instead

### Option A: CloudFlare Headers (Recommended)

When using CloudFlare as your DNS provider with proxy enabled, Foghorn automatically receives geographic headers. No additional Nginx configuration needed.

Foghorn geographic detection priority:
1. CloudFlare headers (highest priority)
2. Standard X-Latitude/X-Longitude headers  
3. MaxMind GeoIP lookups (if configured)
4. Query parameters (lowest priority)

### Option B: Standard GeoIP2 Package

For non-CloudFlare deployments, install the standard GeoIP2 module:

### Prerequisites Installation
```bash
# Arch Linux
pacman -S base-devel gcc meson ninja git wget curl
pacman -S pcre zlib openssl

# Ubuntu/Debian  
apt install build-essential git wget curl
apt install libpcre3-dev zlib1g-dev libssl-dev
```

### MaxMind Database Setup

**1. Install GeoIP Update Tool:**
```bash
# Arch
pacman -S geoipupdate

# Ubuntu/Debian
apt install geoipupdate
```

**2. Configure MaxMind License:**
```bash
# Create GeoIP configuration
nano /etc/GeoIP.conf
```

```ini
# /etc/GeoIP.conf
AccountID YOUR_ACCOUNT_ID
LicenseKey YOUR_LICENSE_KEY
EditionIDs GeoLite2-City GeoLite2-Country GeoLite2-ASN
DatabaseDirectory /etc
```

**3. Set up automatic updates:**
```bash
# Add to crontab
crontab -e

# Add this line for weekly updates (Wednesdays at 2 AM)
0 2 * * 3 /usr/bin/geoipupdate
```

### Custom Nginx Build Process

**1. Build libmaxminddb:**
```bash
mkdir -p ~/nginx && cd ~/nginx
git clone --recursive https://github.com/maxmind/libmaxminddb
cd libmaxminddb/
./bootstrap
./configure
make && make check && make install
ldconfig

# Add library path
echo /usr/local/lib >> /etc/ld.so.conf.d/local.conf
ldconfig
```

**2. Get Nginx source and GeoIP2 module:**
```bash
cd ~/nginx
git clone https://github.com/leev/ngx_http_geoip2_module.git
git clone https://github.com/nginx/nginx.git
cd nginx/
```

**3. Create build configuration:**
```bash
# Create config.sh 
cat > config.sh << 'EOF'
#!/bin/bash
./auto/configure --add-module=../ngx_http_geoip2_module \
--sbin-path=/usr/local/nginx/nginx \
--conf-path=/usr/local/nginx/nginx.conf \
--pid-path=/usr/local/nginx/nginx.pid \
--with-pcre \
--with-zlib=../zlib-1.3.1 \
--with-http_ssl_module \
--with-stream \
--with-http_random_index_module
EOF

chmod +x config.sh
```

**Optional enhanced config (if you need additional features):**
```bash
# Enhanced config with useful extras
cat > config.sh << 'EOF'
#!/bin/bash
./auto/configure --add-module=../ngx_http_geoip2_module \
--sbin-path=/usr/local/nginx/nginx \
--conf-path=/usr/local/nginx/nginx.conf \
--pid-path=/usr/local/nginx/nginx.pid \
--with-pcre \
--with-zlib=../zlib-1.3.1 \
--with-http_ssl_module \
--with-stream \
--with-http_random_index_module \
--with-http_realip_module \
--with-http_stub_status_module \
--with-http_v2_module
EOF

chmod +x config.sh
```

**4. Build and install:**
```bash
./config.sh
make && make install

# Create cache directories
mkdir -p /var/cache/nginx/{client_temp,proxy_temp,fastcgi_temp,uwsgi_temp,scgi_temp}
```

### Systemd Service Configuration

**Create `/etc/systemd/system/nginx.service`:**
```ini
[Unit]
Description=The NGINX HTTP and reverse proxy server
After=syslog.target network.target remote-fs.target nss-lookup.target

[Service]
Type=forking
PIDFile=/usr/local/nginx/nginx.pid
ExecStartPre=/usr/local/nginx/nginx -t
ExecStart=/usr/local/nginx/nginx
ExecReload=/bin/kill -s HUP $MAINPID
ExecStop=/bin/kill -s QUIT $MAINPID
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

**Enable and start:**
```bash
systemctl daemon-reload
systemctl enable --now nginx.service
```

### SSL Certificate Setup with Custom Nginx

**Install Certbot:**
```bash
# Arch
pacman -S certbot

# Ubuntu/Debian
apt install certbot
```

**Generate certificates for custom Nginx:**
```bash
# Stop nginx temporarily
systemctl stop nginx

# Generate certificates
certbot certonly --standalone -d frameworks.network
certbot certonly --standalone -d commodore.frameworks.network
certbot certonly --standalone -d quartermaster.frameworks.network
certbot certonly --standalone -d periscope.frameworks.network
certbot certonly --standalone -d purser.frameworks.network
certbot certonly --standalone -d stats.frameworks.network
certbot certonly --standalone -d forum.frameworks.network
certbot certonly --standalone -d forms.frameworks.network
certbot certonly --standalone -d foghorn.frameworks.network

# Start nginx
systemctl start nginx
```

**Set up certificate renewal:**
```bash
# Add to crontab
crontab -e

# Add this line for certificate renewal (daily check)
0 3 * * * /usr/bin/certbot renew --quiet --post-hook "systemctl reload nginx"
```

### VictoriaMetrics Integration

**Install VictoriaMetrics for high-performance metrics:**
```bash
# Download and install
cd /tmp
wget https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/v1.122.0/victoria-metrics-linux-amd64-v1.122.0.tar.gz
tar -zxvf victoria-metrics-linux-amd64-v1.122.0.tar.gz
mv victoria-metrics-prod /usr/local/bin/
rm victoria-metrics-linux-amd64-v1.122.0.tar.gz

# Create user and directories
useradd -s /usr/sbin/nologin victoriametrics
mkdir -p /var/lib/victoria-metrics
chown -R victoriametrics:victoriametrics /var/lib/victoria-metrics
```

**Create `/etc/systemd/system/victoriametrics.service`:**
```ini
[Unit]
Description=VictoriaMetrics service
After=network.target

[Service]
Type=simple
User=victoriametrics
Group=victoriametrics
ExecStart=/usr/local/bin/victoria-metrics-prod \
  -storageDataPath=/var/lib/victoria-metrics \
  -retentionPeriod=90d \
  -selfScrapeInterval=10s
SyslogIdentifier=victoriametrics
Restart=always
PrivateTmp=yes
ProtectHome=yes
NoNewPrivileges=yes
ProtectSystem=full

[Install]
WantedBy=multi-user.target
```

**Enable VictoriaMetrics:**
```bash
systemctl daemon-reload
systemctl enable --now victoriametrics.service
```

### Grafana Configuration

**Install and configure Grafana:**
```bash
# Arch
pacman -S grafana

# Ubuntu/Debian
apt install grafana
```

**Configure Grafana (`/etc/grafana/grafana.ini`):**
```ini
[server]
http_port = 3000
domain = stats.frameworks.network
root_url = https://stats.frameworks.network/

[security]
admin_user = admin
admin_password = your-secure-password

[auth]
disable_login_form = false

[datasources]
# VictoriaMetrics as default datasource
```

**Enable Grafana:**
```bash
systemctl enable --now grafana.service
```

## Step 6: Production Nginx Configuration

**`/usr/local/nginx/nginx.conf`:**
```nginx
worker_processes  1;
worker_rlimit_nofile 2048;

events {
  worker_connections  1024;
}

http {
  include       mime.types;
  default_type  application/octet-stream;

  sendfile        on;
  keepalive_timeout  65;

  # WebSocket upgrade
  map $http_upgrade $connection_upgrade {
    default upgrade;
    '' close;
  }

  # GeoIP2 binding (install geoip2 module)
  geoip2 /etc/GeoLite2-City.mmdb {
    $geo_lat   location latitude;
    $geo_lon   location longitude;
  }

  # Discourse Forum
  server {
    server_name forum.frameworks.network;
    server_tokens off;

    location / {
      proxy_pass http://unix:/var/discourse/shared/standalone/nginx.http.sock;
      proxy_set_header Host $http_host;
      proxy_http_version 1.1;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_set_header X-Real-IP $remote_addr;
    }

    listen 443 ssl;
    listen [::]:443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Grafana Dashboard (Stats)
  server {
    server_name stats.frameworks.network;
    server_tokens off;
  
    location / {
      proxy_set_header Host $host;
      proxy_pass http://localhost:3000;  # Standard Grafana port
    }

    # Proxy Grafana Live WebSocket connections
    location /api/live/ {
      proxy_http_version 1.1;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection $connection_upgrade;
      proxy_set_header Host $host;
      proxy_pass http://localhost:3000;
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Periscope Analytics API
  server {
    server_name periscope.frameworks.network;
    server_tokens off;
  
    location / {
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_pass http://localhost:9002;  # Correct Periscope port
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Commodore Control API
  server {
    server_name commodore.frameworks.network;
    server_tokens off;
  
    location / {
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_pass http://localhost:9000;  # Correct Commodore port
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Purser Billing API
  server {
    server_name purser.frameworks.network;
    server_tokens off;
  
    location / {
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_pass http://localhost:9007;  # Correct Purser port
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Quartermaster Tenant API
  server {
    server_name quartermaster.frameworks.network;
    server_tokens off;
  
    location / {
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_pass http://localhost:9008;  # Correct Quartermaster port
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Forms API
  server {
    server_name forms.frameworks.network;
    server_tokens off;
  
    location / {
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_pass http://localhost:3002;  # Forms API port
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  # Foghorn Load Balancer
  server {
    server_name foghorn.frameworks.network;
    server_tokens off;

    # Pass GeoIP data to Foghorn for routing decisions
    proxy_set_header X-Latitude $geo_lat;
    proxy_set_header X-Longitude $geo_lon;
  
    location / {
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_pass http://localhost:8080;  # Foghorn port
    }
  
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  ### HTTP -> HTTPS redirects

  server {
    if ($host = forum.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    listen 80; listen [::]:80;
    server_name forum.frameworks.network;
    return 404;
  }

  server {
    if ($host = stats.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name stats.frameworks.network;
    listen 80;
    return 404;
  }

  server {
    if ($host = periscope.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name periscope.frameworks.network;
    listen 80;
    return 404;
  }

  server {
    if ($host = commodore.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name commodore.frameworks.network;
    listen 80;
    return 404;
  }

  server {
    if ($host = purser.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name purser.frameworks.network;
    listen 80;
    return 404;
  }

  server {
    if ($host = quartermaster.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name quartermaster.frameworks.network;
    listen 80;
    return 404;
  }

  server {
    if ($host = forms.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name forms.frameworks.network;
    listen 80;
    return 404;
  }

  server {
    if ($host = foghorn.frameworks.network) {
        return 301 https://$host$request_uri;
    }
    server_name foghorn.frameworks.network;
    listen 80;
    return 404;
  }

  # Redirect unused domains to main site
  server {
    server_name frameport.app frameport.dev frameport.io frameport.nl frameport.online getframes.nl getframes.online;
    return 301 https://frameworks.network$request_uri;
    
    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }

  server {
    if ($host = frameport.app) {
        return 301 https://frameworks.network$request_uri;
    }
    if ($host = frameport.dev) {
        return 301 https://frameworks.network$request_uri;
    }
    if ($host = frameport.io) {
        return 301 https://frameworks.network$request_uri;
    }
    if ($host = frameport.nl) {
        return 301 https://frameworks.network$request_uri;
    }
    if ($host = frameport.online) {
        return 301 https://frameworks.network$request_uri;
    }
    if ($host = getframes.nl) {
        return 301 https://frameworks.network$request_uri;
    }
    if ($host = getframes.online) {
        return 301 https://frameworks.network$request_uri;
    }
    
    listen 80;
    server_name frameport.app frameport.dev frameport.io frameport.nl frameport.online getframes.nl getframes.online;
    return 404;
  }

} # http end
```

### Monitoring Stack Integration

**Grafana Data Source Configuration:**
```json
{
  "name": "VictoriaMetrics",
  "type": "prometheus", 
  "url": "http://localhost:8428",
  "access": "proxy",
  "isDefault": true
}
```

**VictoriaMetrics Remote Write Endpoint:**
```yaml
# For Prometheus remote_write (port 8428)
remote_write:
  - url: http://stats.frameworks.network:8428/api/v1/write
```

### Automated Maintenance Tasks

**Crontab setup (`crontab -e`):**
```bash
# GeoIP database updates (Wednesdays 2 AM)
0 2 * * 3 /usr/bin/geoipupdate

# SSL certificate renewal (Daily 3 AM)  
0 3 * * * /usr/bin/certbot renew --quiet --post-hook "systemctl reload nginx"

# Log rotation and cleanup (Weekly)
0 4 * * 0 /usr/bin/find /var/log/nginx -name "*.log" -mtime +30 -delete
```

### Regional Node Nginx Configuration

Regional nodes use standard nginx with sites-enabled approach for easier management.

**Install nginx:**
```bash
# Ubuntu/Debian
apt install nginx certbot python3-certbot-nginx

# Enable sites-enabled
ln -sf /etc/nginx/sites-available/* /etc/nginx/sites-enabled/
```

**`/etc/nginx/sites-available/website_sales` (Marketing Website):**
```nginx
# Marketing Website - Static Files
server {
    server_name frameworks.network;
    
    root /var/www/monorepo/website_marketing/dist;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    error_page 404 /404.html;
    location = /404.html {
        try_files $uri =404;
    }

    error_page 500 502 503 504 /50x.html;
    location = /50x.html {
        try_files $uri =500;
    }

    listen 443 ssl;
    listen [::]:443 ssl;
    ssl_certificate /etc/letsencrypt/live/frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
}

server {
    if ($host = frameworks.network) {
        return 301 https://$host$request_uri;
    }

    listen 80;
    server_name frameworks.network;
    return 404;
}
```

**`/etc/nginx/sites-available/website_webapp` (SvelteKit App):**
```nginx
# WebApp - Static Files with API Proxy
server {
    server_name app.frameworks.network;
    
    root /var/www/monorepo/website_application/build;
    index index.html;

    # Handle SvelteKit routing
    location / {
        try_files $uri $uri/ @fallback;
    }

    location @fallback {
        rewrite ^.*$ /index.html last;
    }

    # Proxy API calls to central services
    location /api/ {
        proxy_pass https://commodore.frameworks.network;
        proxy_set_header Host commodore.frameworks.network;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    listen 443 ssl;
    listen [::]:443 ssl;
    ssl_certificate /etc/letsencrypt/live/app.frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/app.frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
}

server {
    if ($host = app.frameworks.network) {
        return 301 https://$host$request_uri;
    }

    listen 80;
    server_name app.frameworks.network;
    return 404;
}
```

**`/etc/nginx/sites-available/api_realtime` (Signalman WebSocket API):**
```nginx
# Signalman WebSocket API
server {
    server_name signalman.frameworks.network;
    server_tokens off;

    location / {
        proxy_pass http://localhost:18009;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # WebSocket support
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }

    listen 443 ssl;
    listen [::]:443 ssl;
    ssl_certificate /etc/letsencrypt/live/signalman.frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/signalman.frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
}

server {
    if ($host = signalman.frameworks.network) {
        return 301 https://$host$request_uri;
    }

    listen 80;
    server_name signalman.frameworks.network;
    return 404;
}
```

**`/etc/nginx/sites-available/api_firehose` (Decklog Event API):**

```nginx
# Decklog Event API
server {
    server_name decklog.frameworks.network;
    server_tokens off;
    
    location / {
        proxy_pass http://localhost:9006;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    listen 443 ssl;
    listen [::]:443 ssl;
    ssl_certificate /etc/letsencrypt/live/decklog.frameworks.network/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/decklog.frameworks.network/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
}

server {
    if ($host = decklog.frameworks.network) {
        return 301 https://$host$request_uri;
    }

    listen 80;
    server_name decklog.frameworks.network;
    return 404;
}
```

**Enable sites:**
```bash
# Enable all sites
ln -sf /etc/nginx/sites-available/website_sales /etc/nginx/sites-enabled/
ln -sf /etc/nginx/sites-available/website_webapp /etc/nginx/sites-enabled/
ln -sf /etc/nginx/sites-available/api_realtime /etc/nginx/sites-enabled/
ln -sf /etc/nginx/sites-available/api_firehose /etc/nginx/sites-enabled/

# Test and reload
nginx -t && systemctl reload nginx
```

**Generate SSL certificates:**
```bash
certbot --nginx -d frameworks.network
certbot --nginx -d app.frameworks.network  
certbot --nginx -d signalman.frameworks.network
certbot --nginx -d decklog.frameworks.network
```

### Static File Deployment

**Create deployment directories:**
```bash
mkdir -p /var/www/monorepo
chown -R www-data:www-data /var/www/monorepo
```

**Build and deploy websites:**
```bash
# Marketing website (React/Vite)
cd /opt/frameworks/monorepo/website_marketing
npm run build
cp -r dist/* /var/www/monorepo/website_marketing/dist/

# WebApp (SvelteKit)  
cd /opt/frameworks/monorepo/website_application
npm run build
cp -r build/* /var/www/monorepo/website_application/build/
```

### SSL Certificate Generation
```bash
# Generate certificates for all domains
certbot --nginx -d commodore.frameworks.network
certbot --nginx -d quartermaster.frameworks.network
certbot --nginx -d periscope.frameworks.network
certbot --nginx -d purser.frameworks.network
certbot --nginx -d frameworks.network
certbot --nginx -d app.frameworks.network
certbot --nginx -d signalman.frameworks.network
certbot --nginx -d decklog.frameworks.network

# Set up auto-renewal
systemctl enable certbot.timer
```

## Step 6: Service Deployment

### Build & Deploy Script Example
```bash
#!/bin/bash
# deploy-central.sh

set -e

# Build services
cd /opt/frameworks/monorepo

# Bridge
cd api_gateway
go build -o bridge ./cmd/bridge
sudo systemctl stop bridge || true
sudo cp bridge /usr/local/bin/
sudo systemctl start bridge

# Commodore
cd ../api_control
go build -o commodore ./cmd/api
sudo systemctl stop commodore || true
sudo cp commodore /usr/local/bin/
sudo systemctl start commodore

# Quartermaster  
cd ../api_tenants
go build -o quartermaster ./cmd/quartermaster
sudo systemctl stop quartermaster || true
sudo cp quartermaster /usr/local/bin/
sudo systemctl start quartermaster

# Periscope
cd ../api_analytics_query
go build -o periscope-query ./cmd/periscope
sudo systemctl stop periscope-query || true
sudo cp periscope-query /usr/local/bin/
sudo systemctl start periscope-query

# Purser
cd ../api_billing
go build -o purser ./cmd/purser
sudo systemctl stop purser || true
sudo cp purser /usr/local/bin/
sudo systemctl start purser

# Foghorn
cd ../api_balancing
go build -o foghorn ./cmd/foghorn
sudo systemctl stop foghorn || true
sudo cp foghorn /usr/local/bin/
sudo systemctl start foghorn

echo "Central services deployed successfully!"
```

### Systemd Service Example

**`/etc/systemd/system/bridge.service`:**
```ini
[Unit]
Description=FrameWorks Bridge API Gateway
After=network.target

[Service]
Type=simple
User=frameworks
Group=frameworks
WorkingDirectory=/opt/frameworks/bridge
Environment=BRIDGE_PORT=18000
Environment=GIN_MODE=release
Environment=LOG_LEVEL=info
Environment=GRAPHQL_PLAYGROUND_ENABLED=false
Environment=GRAPHQL_COMPLEXITY_LIMIT=200
Environment=COMMODORE_URL=http://127.0.0.1:18001
Environment=PERISCOPE_QUERY_URL=http://127.0.0.1:18004
Environment=PURSER_URL=http://127.0.0.1:18003
Environment=SIGNALMAN_WS_URL=ws://127.0.0.1:18009
Environment=JWT_SECRET=your-jwt-secret-here
Environment=SERVICE_TOKEN=your-service-token-here
ExecStart=/opt/frameworks/bridge/bridge
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/commodore.service`:**
```ini
[Unit]
Description=FrameWorks Commodore Service
After=network.target

[Service]
Type=simple
User=frameworks
Group=frameworks
WorkingDirectory=/opt/frameworks/commodore
Environment=PORT=18001
Environment=GIN_MODE=release
Environment=LOG_LEVEL=info
Environment=DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require
Environment=JWT_SECRET=your-jwt-secret-here
Environment=SERVICE_TOKEN=your-service-token-here
Environment=FOGHORN_URLS=http://127.0.0.1:18008
Environment=FOGHORN_URL=http://127.0.0.1:18008
Environment=FOGHORN_CONTROL_ADDR=:18019
Environment=MIST_USERNAME=test
Environment=MIST_PASSWORD=test
Environment=QUARTERMASTER_URL=http://127.0.0.1:18002
ExecStart=/opt/frameworks/commodore/commodore
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/quartermaster.service`:**
```ini
[Unit]
Description=FrameWorks Quartermaster Service
After=network.target

[Service]
Type=simple
User=frameworks
Group=frameworks
WorkingDirectory=/opt/frameworks/quartermaster
Environment=PORT=18002
Environment=GIN_MODE=release
Environment=LOG_LEVEL=info
Environment=DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require
Environment=JWT_SECRET=your-jwt-secret-here
Environment=SERVICE_TOKEN=your-service-token-here
ExecStart=/opt/frameworks/quartermaster/quartermaster
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/periscope.service`:**
```ini
[Unit]
Description=FrameWorks Periscope Service
After=network.target

[Service]
Type=simple
User=frameworks
Group=frameworks
WorkingDirectory=/opt/frameworks/periscope
Environment=PORT=18004
Environment=GIN_MODE=release
Environment=LOG_LEVEL=info
Environment=DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require
Environment=JWT_SECRET=your-jwt-secret-here
Environment=SERVICE_TOKEN=your-service-token-here
Environment=PURSER_URL=http://127.0.0.1:18003
Environment=BILLING_HOURLY_INTERVAL=60
Environment=BILLING_DAILY_INTERVAL=24
Environment=CLICKHOUSE_HOST=clickhouse.frameworks.network
Environment=CLICKHOUSE_PORT=8123
Environment=CLICKHOUSE_DB=frameworks
Environment=CLICKHOUSE_USER=frameworks
Environment=CLICKHOUSE_PASSWORD=frameworks_dev
ExecStart=/opt/frameworks/periscope/periscope
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/purser.service`:**
```ini
[Unit]
Description=FrameWorks Purser Service
After=network.target

[Service]
Type=simple
User=frameworks
Group=frameworks
WorkingDirectory=/opt/frameworks/purser
Environment=PORT=18003
Environment=GIN_MODE=release
Environment=LOG_LEVEL=info
Environment=DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require
Environment=JWT_SECRET=your-jwt-secret-here
Environment=SERVICE_TOKEN=your-service-token-here
Environment=STRIPE_SECRET_KEY=sk_test_your_key_here
Environment=STRIPE_WEBHOOK_SECRET=whsec_your_webhook_secret_here
ExecStart=/opt/frameworks/purser/purser
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/foghorn.service`:**
```ini
[Unit]
Description=FrameWorks Foghorn Service
After=network.target

[Service]
Type=simple
User=frameworks
Group=frameworks
WorkingDirectory=/opt/frameworks/foghorn
Environment=PORT=18008
Environment=GIN_MODE=release
Environment=LOG_LEVEL=info
Environment=DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require
Environment=JWT_SECRET=your-jwt-secret-here
Environment=SERVICE_TOKEN=your-service-token-here
Environment=QUARTERMASTER_URL=http://127.0.0.1:18002
Environment=MIST_USERNAME=test
Environment=MIST_PASSWORD=test
Environment=DECKLOG_GRPC_TARGET=127.0.0.1:18006
Environment=DECKLOG_ALLOW_INSECURE=false
ExecStart=/opt/frameworks/foghorn/foghorn
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Step 7: Environment Variables

### Central Node
```bash
# /opt/frameworks/.env

# YugabyteDB
DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require

# ClickHouse
CLICKHOUSE_HOST=clickhouse.frameworks.network
CLICKHOUSE_PORT=8123
CLICKHOUSE_DB=frameworks
CLICKHOUSE_USER=frameworks
CLICKHOUSE_PASSWORD=frameworks_dev

# Authentication
JWT_SECRET=your-super-secret-jwt-key
SERVICE_TOKEN=your-service-to-service-token

# External Services
STRIPE_PUBLISHABLE_KEY=pk_live_...
STRIPE_SECRET_KEY=sk_live_...
MOLLIE_API_KEY=live_...

# Email
SMTP_HOST=smtp.fastmail.com
SMTP_PORT=587
SMTP_USER=info@frameworks.network
SMTP_PASS=your-app-password

# Internal URLs
QUARTERMASTER_URL=http://127.0.0.1:9008
PURSER_URL=http://127.0.0.1:9007
```

### Regional Node (Hetzner)
```bash
# /opt/frameworks/.env

# Database
DATABASE_URL=postgres://frameworks_user:frameworks_dev@db.frameworks.network:5433/frameworks?sslmode=require

# ClickHouse
CLICKHOUSE_HOST=clickhouse.frameworks.network
CLICKHOUSE_PORT=8123
CLICKHOUSE_DB=frameworks
CLICKHOUSE_USER=frameworks
CLICKHOUSE_PASSWORD=frameworks_dev

# Kafka
KAFKA_BOOTSTRAP=10.10.0.1:9092,10.10.0.2:9092,10.10.0.3:9092
KAFKA_CLUSTER_ID=frameworks-kafka-cluster

# Region
REGION=eu-west
CLUSTER_ID=frameworks-regional-eu-1

# External APIs
COMMODORE_URL=https://commodore.frameworks.network
QUARTERMASTER_URL=https://quartermaster.frameworks.network
PURSER_URL=https://purser.frameworks.network

# Authentication
SERVICE_TOKEN=your-service-to-service-token
```

## Step 8: Monitoring & Health Checks

### Cloudflare Health Checks
Configure health check pools in Cloudflare:

```json
{
  "name": "frameworks-central-pool",
  "origins": [
    {
      "name": "central-primary",
      "address": "136.144.189.92",
      "enabled": true,
      "weight": 1
    }
  ],
  "monitor": {
    "type": "https",
    "method": "GET",
    "path": "/health",
    "expected_codes": "200",
    "interval": 60,
    "timeout": 5,
    "retries": 2
  }
}
```

### Prometheus Configuration
```yaml
# /opt/prometheus/prometheus.yml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: 'frameworks-central'
    static_configs:
      - targets: ['127.0.0.1:9000', '127.0.0.1:9002', '127.0.0.1:9007', '127.0.0.1:9008']
    metrics_path: '/metrics'
    
  - job_name: 'frameworks-regional'
    static_configs:
      - targets: ['10.10.0.2:18032', '10.10.0.2:9006', '10.10.0.3:18032', '10.10.0.3:9006']
    metrics_path: '/metrics'
    
  - job_name: 'yugabyte'
    static_configs:
      - targets: ['10.10.0.11:7000', '10.10.0.12:7000', '10.10.0.13:7000']
    metrics_path: '/prometheus-metrics'

  - job_name: 'clickhouse'
    static_configs:
      - targets: ['10.10.0.11:8123', '10.10.0.12:8123', '10.10.0.13:8123']
    metrics_path: '/metrics'
```

## Step 9: Security Hardening

### Firewall Rules
```bash
# Central node firewall
ufw default deny incoming
ufw default allow outgoing
ufw allow ssh
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow 51820/udp  # WireGuard
ufw allow from 10.10.0.0/24  # Mesh network
ufw enable

# Database nodes - only mesh traffic
ufw default deny incoming
ufw default allow outgoing
ufw allow ssh
ufw allow 51820/udp  # WireGuard
ufw allow from 10.10.0.0/24 to any port 7000,7100,9000,9100  # YugabyteDB
ufw allow from 10.10.0.0/24 to any port 8123,9000  # ClickHouse
ufw enable
```
