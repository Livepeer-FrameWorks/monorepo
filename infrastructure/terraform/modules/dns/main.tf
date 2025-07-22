resource "cloudflare_zone" "frameworks" {
  zone = var.domain_name
}

# API and control plane records
resource "cloudflare_record" "api" {
  zone_id = cloudflare_zone.frameworks.id
  name    = "api"
  value   = var.central_cluster.ip
  type    = "A"
  ttl     = 300
  proxied = true
}

resource "cloudflare_record" "control" {
  for_each = toset([
    "commodore",
    "quartermaster",
    "periscope",
    "purser",
    "foghorn"
  ])

  zone_id = cloudflare_zone.frameworks.id
  name    = each.key
  value   = var.central_cluster.ip
  type    = "A"
  ttl     = 300
  proxied = true
}

# Regional records
resource "cloudflare_record" "regional" {
  count = length(var.regional_clusters)

  zone_id = cloudflare_zone.frameworks.id
  name    = "regional-${var.regional_clusters[count.index].region}"
  value   = var.regional_clusters[count.index].ip
  type    = "A"
  ttl     = 300
  proxied = true
}

# Edge records
resource "cloudflare_record" "edge" {
  count = length(var.edge_clusters)

  zone_id = cloudflare_zone.frameworks.id
  name    = "edge-${var.edge_clusters[count.index].region}"
  value   = var.edge_clusters[count.index].ip
  type    = "A"
  ttl     = 300
  proxied = true
}

# Global load balancer
resource "cloudflare_load_balancer" "global" {
  zone_id = cloudflare_zone.frameworks.id
  name    = "global-lb"
  
  default_pool_ids = [
    cloudflare_load_balancer_pool.central.id,
    cloudflare_load_balancer_pool.regional.id
  ]
  
  fallback_pool_id = cloudflare_load_balancer_pool.central.id
  
  description = "Global load balancer for FrameWorks"
  proxied = true
  enabled = true
  
  rules {
    name = "geo-routing"
    condition = "ip.geo.continent == \"EU\""
    fixed_response {
      message_body = ""
      status_code  = 302
      location     = "https://regional-eu.${var.domain_name}"
    }
  }
}

# Load balancer pools
resource "cloudflare_load_balancer_pool" "central" {
  name = "central-pool"
  origins {
    name    = "central-primary"
    address = var.central_cluster.ip
    enabled = true
    weight  = 1
  }
  monitor = cloudflare_load_balancer_monitor.http_monitor.id
}

resource "cloudflare_load_balancer_pool" "regional" {
  name = "regional-pool"
  
  dynamic "origins" {
    for_each = var.regional_clusters
    content {
      name    = "regional-${origins.value.region}"
      address = origins.value.ip
      enabled = true
      weight  = 1
    }
  }
  
  monitor = cloudflare_load_balancer_monitor.http_monitor.id
}

# Health monitor
resource "cloudflare_load_balancer_monitor" "http_monitor" {
  type           = "http"
  method         = "GET"
  path           = "/health"
  port           = 443
  interval       = 60
  retries        = 2
  timeout        = 5
  expected_codes = "200"
} 