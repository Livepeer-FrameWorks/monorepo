# 🏗️ FrameWorks Infrastructure Implementation

Implementation details for FrameWorks' infrastructure code.

> **⚠️** 🐉
> Our MVP is manually provisioned. The Terraform and Ansible infra files are AI-generated.
> Well fix these docs and example configs as we scale up ourselves.


## 📋 Repository Structure

```
infrastructure/
├── terraform/              # Machine provisioning
│   ├── environments/      # Environment-specific configs
│   │   ├── prod/
│   │   │   ├── main.tf   # Production environment
│   │   │   ├── variables.tf
│   │   │   └── terraform.tfvars
│   │   └── staging/
│   ├── modules/          # Reusable components
│   │   ├── cluster/     # VM cluster management
│   │   ├── dns/        # DNS and load balancing
│   │   └── certificates/ # TLS certificate management
│   └── providers/      # Provider configurations
│       ├── hetzner/
│       ├── cloudflare/
│       └── acme/
├── ansible/             # Configuration management
│   ├── inventory/      # Environment-specific inventory
│   │   ├── prod/
│   │   │   ├── hosts.yml
│   │   │   └── group_vars/
│   │   └── staging/
│   ├── playbooks/     # Task organization
│   │   ├── site.yml  # Main entry point
│   │   ├── infrastructure.yml
│   │   ├── services.yml
│   │   └── monitoring.yml
│   └── roles/        # Reusable configurations
│       ├── common/   # Base system setup
│       ├── wireguard/ # Mesh networking
│       ├── frameworks-api/
│       ├── frameworks-media/
│       └── monitoring/
├── prometheus/          # Prometheus configuration
│   ├── prometheus.yml  # Main config
│   └── rules/         # Alerting rules
│       └── frameworks.yml
├── grafana/            # Grafana configuration
│   ├── provisioning/  # Auto-provisioning
│   │   ├── datasources/
│   │   └── dashboards/
│   └── dashboards/    # Dashboard definitions
│       ├── frameworks-overview.json
│       └── infrastructure-metrics.json
└── scripts/          # Infrastructure tooling
```

## 🚀 Usage

### Prerequisites

Install required tools:
```bash
# Terraform
curl -fsSL https://apt.releases.hashicorp.com/gpg | sudo apt-key add -
sudo apt-add-repository "deb [arch=amd64] https://apt.releases.hashicorp.com $(lsb_release -cs) main"
sudo apt-get update && sudo apt-get install terraform

# Ansible
sudo apt-get install ansible

# Additional tools for cloud providers
pip install hcloud  # For Hetzner Cloud
```

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

### Machine Provisioning (Terraform)

```bash
# Initialize environment
cd terraform/environments/staging
terraform init -backend-config=backend.hcl

# Plan changes
terraform plan -var-file=staging.tfvars

# Apply changes
terraform apply -var-file=staging.tfvars

# Common operations
terraform apply -var node_count=4          # Add new node
terraform apply -target=module.dns         # Update DNS
terraform taint module.certificates["api"] # Rotate cert
```

### Configuration Management (Ansible)

```bash
# Full site deployment
cd ansible
ansible-playbook -i inventory/prod playbooks/site.yml

# Infrastructure only
ansible-playbook -i inventory/prod playbooks/infrastructure.yml

# Service deployment
ansible-playbook -i inventory/prod playbooks/services.yml

# Common operations
ansible-playbook -i inventory/prod playbooks/infrastructure.yml --limit new-node.frameworks.network
ansible-playbook -i inventory/prod playbooks/services.yml --tags config
ansible-playbook -i inventory/prod playbooks/infrastructure.yml --tags wireguard
```

## 📦 Example Configurations

### Terraform Module

```hcl
# terraform/modules/cluster/main.tf

locals {
  node_labels = {
    central  = { role = "control" }
    regional = { role = "data" }
    edge     = { role = "media" }
  }
}

resource "hcloud_server" "node" {
  count       = var.node_count
  name        = "frameworks-${var.cluster_type}-${count.index + 1}"
  server_type = var.server_type
  image       = var.image
  location    = var.location
  
  network {
    network_id = hcloud_network.frameworks.id
    ip         = cidrhost(var.subnet_cidr, count.index + 10)
  }
  
  labels = merge(
    local.node_labels[var.cluster_type],
    var.additional_labels
  )
}
```

### Ansible Role

```yaml
# ansible/roles/frameworks-api/tasks/main.yml
- name: Create service user
  user:
    name: "{{ item.name }}"
    system: yes
    create_home: no
    shell: /usr/sbin/nologin
  loop: "{{ frameworks_services }}"

- name: Copy service binary
  copy:
    src: "files/{{ item.name }}"
    dest: "/usr/local/bin/{{ item.name }}"
    mode: '0755'
    owner: "{{ item.name }}"
    group: "{{ item.name }}"
  loop: "{{ frameworks_services }}"

- name: Configure systemd service
  template:
    src: service.j2
    dest: "/etc/systemd/system/{{ item.name }}.service"
    mode: '0644'
  loop: "{{ frameworks_services }}"
  notify: reload systemd
```

## 🔄 Integration Points

### Quartermaster Integration

```python
#!/usr/bin/env python3
"""
Ansible dynamic inventory script that combines Terraform outputs
with Quartermaster API data
"""
import json
import requests

def get_inventory():
    response = requests.get('http://quartermaster:9008/api/inventory')
    inventory = response.json()
    
    ansible_inventory = {
        '_meta': {'hostvars': {}},
        'all': {'children': ['control', 'data', 'media']},
        'control': {'hosts': []},
        'data': {'hosts': []},
        'media': {'hosts': []}
    }
    
    for node in inventory['nodes']:
        group = node['node_type']
        if group in ansible_inventory:
            ansible_inventory[group]['hosts'].append(node['node_id'])
            ansible_inventory['_meta']['hostvars'][node['node_id']] = {
                'ansible_host': node['internal_ip'],
                'cluster_id': node['cluster_id'],
                'region': node['region']
            }
    
    return ansible_inventory

if __name__ == '__main__':
    print(json.dumps(get_inventory(), indent=2))
``` 