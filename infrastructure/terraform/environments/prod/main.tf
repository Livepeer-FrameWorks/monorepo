terraform {
  required_providers {
    hcloud = {
      source = "hetznercloud/hcloud"
    }
    cloudflare = {
      source = "cloudflare/cloudflare"
    }
    acme = {
      source = "vancluever/acme"
    }
  }
}

provider "hcloud" {
  token = var.hcloud_token
}

provider "cloudflare" {
  api_token = var.cloudflare_token
}

provider "acme" {
  server_url = var.acme_server_url
}

# Central cluster
module "central_cluster" {
  source = "../../modules/cluster"

  cluster_type  = "central"
  node_count    = 3
  server_type   = "cx41" # 8 vCPU, 16GB RAM
  location      = "fsn1" # Frankfurt
  network_cidr  = "10.0.1.0/24"
  
  labels = {
    environment = "prod"
    role        = "control"
  }
}

# Regional EU cluster
module "regional_eu_cluster" {
  source = "../../modules/cluster"

  cluster_type  = "regional"
  node_count    = 2
  server_type   = "cx31" # 4 vCPU, 8GB RAM
  location      = "hel1" # Helsinki
  network_cidr  = "10.0.2.0/24"
  
  labels = {
    environment = "prod"
    role        = "data"
    region      = "eu"
  }
}

# Edge clusters
module "edge_us_east_cluster" {
  source = "../../modules/cluster"

  cluster_type  = "edge"
  node_count    = 1
  server_type   = "cx21" # 2 vCPU, 4GB RAM
  location      = "ash"  # Ashburn
  network_cidr  = "10.0.3.0/24"
  
  labels = {
    environment = "prod"
    role        = "media"
    region      = "us-east"
  }
}

# DNS and Load Balancing
module "dns" {
  source = "../../modules/dns"

  domain_name = "frameworks.network"
  
  central_cluster = {
    ip      = module.central_cluster.public_ip
    region  = "eu-central"
  }
  
  regional_clusters = [
    {
      ip      = module.regional_eu_cluster.public_ip
      region  = "eu-north"
    }
  ]
  
  edge_clusters = [
    {
      ip      = module.edge_us_east_cluster.public_ip
      region  = "us-east"
    }
  ]
}

# TLS Certificates
module "certificates" {
  source = "../../modules/certificates"

  cloudflare_token = var.cloudflare_token
  email           = "certs@frameworks.dev"
  
  domains = {
    "frameworks.network" = {
      sans = [
        "*.frameworks.network",
        "api.frameworks.network",
        "edge.frameworks.network"
      ]
    }
  }
  
  depends_on = [module.dns]
} 