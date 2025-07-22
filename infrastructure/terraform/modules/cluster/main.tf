locals {
  node_labels = {
    central  = { role = "control" }
    regional = { role = "data" }
    edge     = { role = "media" }
  }
}

resource "hcloud_network" "cluster" {
  name     = "frameworks-${var.cluster_type}"
  ip_range = var.network_cidr
}

resource "hcloud_network_subnet" "cluster" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = var.network_cidr
}

resource "hcloud_server" "node" {
  count       = var.node_count
  name        = "frameworks-${var.cluster_type}-${count.index + 1}"
  server_type = var.server_type
  image       = var.image
  location    = var.location
  
  network {
    network_id = hcloud_network.cluster.id
    ip         = cidrhost(var.network_cidr, count.index + 10)
  }
  
  labels = merge(
    local.node_labels[var.cluster_type],
    var.labels
  )

  user_data = templatefile("${path.module}/templates/cloud-init.yml", {
    hostname = "frameworks-${var.cluster_type}-${count.index + 1}"
    ssh_keys = var.ssh_public_keys
  })
}

resource "hcloud_firewall" "cluster" {
  name = "frameworks-${var.cluster_type}"
  
  # SSH access
  rule {
    direction = "in"
    protocol  = "tcp"
    port      = "22"
    source_ips = ["0.0.0.0/0"]
  }
  
  # HTTP/HTTPS
  rule {
    direction = "in"
    protocol  = "tcp"
    port      = "80"
    source_ips = ["0.0.0.0/0"]
  }
  
  rule {
    direction = "in"
    protocol  = "tcp"
    port      = "443"
    source_ips = ["0.0.0.0/0"]
  }
  
  # WireGuard
  rule {
    direction = "in"
    protocol  = "udp"
    port      = "51820"
    source_ips = ["0.0.0.0/0"]
  }
  
  # Internal traffic
  rule {
    direction = "in"
    protocol  = "tcp"
    port      = "any"
    source_ips = [var.network_cidr]
  }
  
  rule {
    direction = "in"
    protocol  = "udp"
    port      = "any"
    source_ips = [var.network_cidr]
  }
}

# Output WireGuard keys for Ansible
resource "tls_private_key" "wireguard" {
  count     = var.node_count
  algorithm = "ECDSA"
  ecdsa_curve = "P521"
}

resource "local_file" "wireguard_private_key" {
  count    = var.node_count
  content  = tls_private_key.wireguard[count.index].private_key_pem
  filename = "${path.module}/keys/${hcloud_server.node[count.index].name}.key"
} 