# 🏗️ FrameWorks Infrastructure Configurations

Configuration files used by FrameWorks services across deployment methods.

## Contents

- **`mistserver.conf`** - MistServer media server configuration
- **`nginx/`** - Reverse proxy configurations  
- **`clickhouse/`** - ClickHouse database settings and users
- **`prometheus/`** - Metrics collection and alerting rules
- **`grafana/`** - Dashboards and data source provisioning
- **`frameworks.service`** - Systemd unit template (bare metal deployments)

These files are automatically used by `docker-compose up` and referenced by bare metal deployment guides.

## 📊 Monitoring Stack

FrameWorks includes a comprehensive monitoring setup with Prometheus and Grafana for observability.

### Components

- **Prometheus** (`localhost:9091`) - Metrics collection and alerting
- **Grafana** (`localhost:3000`) - Visualization and dashboards
- **ClickHouse** - Time-series analytics data
- **PostgreSQL** - State and configuration data

### Access

- **Grafana UI**: http://localhost:3000
  - Username: `admin`
  - Password: `frameworks_dev`
- **Prometheus UI**: http://localhost:9091

### Dashboards

The monitoring stack includes pre-configured dashboards:

1. **FrameWorks Overview** - High-level streaming metrics
   - Active viewers and streams
   - Geographic distribution
   - Service availability
   - Bandwidth usage

2. **Infrastructure Metrics** - System-level monitoring
   - CPU and memory usage
   - Network connections
   - Load balancer events
   - Database performance

### Data Sources

- **Prometheus**: Service metrics, health checks, system resources
- **ClickHouse**: Real-time analytics, viewer metrics, connection events
- **PostgreSQL**: Configuration data, user management, billing

### Alerting

Basic alerting rules are configured for:
- Service downtime
- High CPU/memory usage
- Stream latency issues
- Database connection limits
- Kafka consumer lag

### Customization

Dashboard and alert configurations are stored in:
- `infrastructure/grafana/dashboards/` - Dashboard JSON files
- `infrastructure/prometheus/rules/` - Alerting rules
- `infrastructure/grafana/provisioning/` - Auto-provisioning configs
