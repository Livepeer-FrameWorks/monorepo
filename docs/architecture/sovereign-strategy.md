# Sovereign Architecture Strategy

FrameWorks is designed to run entirely on customer infrastructure without vendor lock-in. This document explains why Navigator and Privateer exist.

**Deployment Models**:

- **Shared SaaS**: Multi-tenant clusters on our infrastructure
- **Dedicated SaaS**: Per-tenant clusters on our infrastructure
- **Self-Hosted**: Full deployment on customer premises (B2B/government)

---

## Why Navigator Exists

Every paying customer needs:

- Custom subdomain (`customer.frameworks.network`)
- Per-tenant load balancer endpoint
- Automatic TLS certificate provisioning
- DNS failover to backup clusters

**Terraform/Ansible cannot solve this** because:

1. Tenants self-service provision domains (no human runs `terraform apply`)
2. DNS records change dynamically based on node health
3. Certificate issuance is per-tenant, not per-cluster

### What Navigator Does

```
Tenant Signs Up (Pro Tier)
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│                      Navigator                              │
├─────────────────────────────────────────────────────────────┤
│ 1. Create DNS record: customer.frameworks.network → LB IP  │
│ 2. Issue TLS cert via ACME DNS-01 challenge                │
│ 3. Store cert in tenant-scoped storage                     │
│ 4. Configure edge nodes to serve cert                      │
│ 5. Monitor cert expiry, auto-renew                         │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
Customer streams to: rtmp://customer.frameworks.network/live
Viewers watch at: https://customer.frameworks.network/play/{playback-id}/hls/index.m3u8
```

### Navigator Roadmap

| Phase | Capability                                 | Status                    |
| ----- | ------------------------------------------ | ------------------------- |
| **1** | Cloudflare DNS API integration             | Implemented               |
| **2** | ACME certificate issuance (Let's Encrypt)  | Implemented               |
| **3** | Tenant subdomain provisioning/verification | Planned                   |
| **4** | Self-hosted DNS (PowerDNS)                 | See `rfcs/dns-anycast.md` |
| **5** | Bring-your-own-certificate support         | Planned                   |

---

## Why Privateer Exists

FrameWorks infrastructure spans central services, regional services, and edge nodes. These need secure, private connectivity.

| Approach              | Pros                           | Cons                           |
| --------------------- | ------------------------------ | ------------------------------ |
| Public internet + TLS | Simple                         | Exposed attack surface         |
| Cloud VPC             | Managed                        | Vendor lock-in, single cloud   |
| Tailscale             | Zero-config                    | SaaS dependency, cost at scale |
| Headscale             | Self-hosted Tailscale          | External project dependency    |
| **Privateer**         | Full control, tenant isolation | Custom development             |

### Why Not Tailscale/Headscale?

**Sovereignty requirement**: We cannot depend on external SaaS for critical infrastructure.

- Tailscale coordination server is SaaS
- Network topology visible to Tailscale Inc.
- Pricing scales with device count
- Cannot run on air-gapped customer premises
- Headscale introduces external project dependency
- Neither has native per-tenant isolation

### What Privateer Enables

**Phase 1 (Current)**: Per-cluster shared mesh

```
┌─────────────────────────────────────────────────────────────┐
│                    WireGuard Mesh (10.200.0.0/16)           │
│                                                             │
│   ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐  │
│   │ Central │◄──►│ Regional│◄──►│  Edge   │◄──►│  Edge   │  │
│   │ EU-1    │    │  US-1   │    │  NYC    │    │  LAX    │  │
│   └─────────┘    └─────────┘    └─────────┘    └─────────┘  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

**Phase 2 (B2B)**: Per-tenant cluster isolation

```
┌─────────────────────────────────────────────────────────────┐
│                    Shared Mesh (platform)                   │
│   Central services, shared regional                         │
└─────────────────────────────────────────────────────────────┘
              │
              │ Peering
              ▼
┌─────────────────────────────────────────────────────────────┐
│              Tenant A Mesh (isolated)                       │
│   Dedicated edge nodes, isolated traffic                    │
└─────────────────────────────────────────────────────────────┘
              │
              │ No connectivity
              ▼
┌─────────────────────────────────────────────────────────────┐
│              Tenant B Mesh (isolated)                       │
│   Completely isolated from Tenant A                         │
└─────────────────────────────────────────────────────────────┘
```

### Privateer Roadmap

| Phase | Capability                     | Status                       |
| ----- | ------------------------------ | ---------------------------- |
| **1** | Single full-mesh topology      | In Testing                   |
| **2** | Token-based node enrollment    | Implemented                  |
| **3** | Local DNS for mesh hostnames   | Implemented                  |
| **4** | WireGuard-OSPF dynamic routing | See `rfcs/wireguard-ospf.md` |
| **5** | Per-tenant mesh segments       | See `rfcs/mesh-isolation.md` |

---

## Related RFCs

- `rfcs/dns-anycast.md` - Self-hosted global anycast DNS
- `rfcs/wireguard-ospf.md` - Dynamic mesh routing
- `rfcs/mesh-isolation.md` - Per-tenant network segments
