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

| Phase | Capability                                                       | Status                    |
| ----- | ---------------------------------------------------------------- | ------------------------- |
| **1** | Cloudflare DNS API integration                                   | Implemented               |
| **2** | ACME certificate issuance (Let's Encrypt)                        | Implemented               |
| **3** | Per-cluster DNS + per-edge A records + wildcard TLS (ConfigSeed) | Implemented               |
| **4** | Self-hosted DNS (PowerDNS)                                       | See `rfcs/dns-anycast.md` |
| **5** | Bring-your-own-certificate support                               | Planned                   |

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

---

## Operator Infrastructure Playbook

Guidance for operators deploying FrameWorks on sovereign infrastructure.

### Facility Selection

Choose **Network and Carrier-Neutral Exchange** facilities over hyperscaler campuses. These house multiple ISPs and Internet Exchange Points, providing lowest latency to eyeball networks.

Evaluation checklist:

- **IX presence**: Internet Exchange Points in the facility reduce hops to major ISPs
- **Tier-1 ISP diversity**: Multiple upstream transit providers for redundancy
- **Financial stability**: Research the datacenter REIT's financial health (facility closures disrupt operations)
- **Physical security**: Access controls, surveillance, security personnel
- **Loading dock access**: Shipping and receiving procedures for equipment deliveries
- **Floor weight loads**: Verify cabinets support your hardware density
- **Fuel autonomy**: Days of generator runtime (10+ days is ideal)
- **Smart hands**: On-site staff who can swap components or power-cycle equipment in emergencies
- **Parent lease risk**: For subdivided buildings, verify the primary leaseholder's stability

### Edge Placement Strategy

Foghorn's viewer routing scores edges by geographic distance (haversine, H3 resolution 5) and bandwidth (weight 1000 each — the two heaviest factors). Placing edges at IX-adjacent facilities directly optimizes these scores:

- Fewer network hops to eyeball networks = lower viewer latency
- IX peering = better bandwidth utilization scores
- Carrier-neutral facilities often have direct peering with major CDNs and ISPs

Haversine distance matters, but network topology matters more. An edge 100km away at a well-connected IX may score better than an edge 50km away behind multiple transit hops.

### Capacity Planning (N×2 Rule)

Never exceed 50% utilization on any resource (CPU, RAM, bandwidth). This ensures:

- Headroom for traffic spikes (viral streams, concurrent events)
- Graceful degradation if an edge goes offline (remaining edges absorb load without saturation)
- Maintenance windows without impacting service

Foghorn's scoring deprioritizes saturated nodes via gradient scoring but doesn't exclude them. Operators should provision enough edges that the gradient never reaches the danger zone. See `rfcs/capacity-planning.md` for proposed configurable exclusion thresholds.

### Hardware Diversity

- Track every SKU: CPUs, NICs, drives, transceivers. Use Quartermaster's `infrastructure_nodes` for node-level inventory; consider Netbox for component-level tracking
- Maintain cold spares: "Two is one, one is none." Keep replacement components racked and unplugged, or binned at the facility
- Diversify vendors: Don't run all edges on identical hardware. A firmware bug or supply chain issue in one vendor shouldn't take down the fleet
- Buy N-1 generation: Current-generation hardware at release pricing is rarely justified. Previous-generation components at steep discounts provide better value

### Bus Factor of 2

Every operational procedure must be documented. Every credential must be accessible by at least two people.

No single person should be the only one who can:

- Access a facility
- Rotate a certificate or credential
- Restart a critical service
- Perform a database migration
- Respond to an incident

### Why Sovereign Infrastructure Matters

The consolidation towards hyperscalers shapes how engineers think about architecture. A generation of developers is trained not to build what is right, but what is easy because there is a managed service for it.

FrameWorks exists to break that constraint. Sovereign infrastructure enables capabilities that are impossible or impractical on managed platforms:

- Custom NAT traversal coordination (see `rfcs/nat-traversal.md`)
- TLS fingerprinting at the edge for fraud detection (see `rfcs/network-security-capabilities.md`)
- DNS query logging for adversary tracking (see `rfcs/dns-anycast.md`)
- Network-level security techniques that require controlling the TLS termination point

Operators who run FrameWorks on their own infrastructure aren't just saving money — they're gaining capabilities that differentiate their service.
