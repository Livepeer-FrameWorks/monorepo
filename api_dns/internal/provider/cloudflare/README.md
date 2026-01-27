# CloudFlare API Client

Go client for CloudFlare API v4, focused on DNS and Load Balancer management for FrameWorks infrastructure.

## Features

- **DNS Records**: Create, update, list, and delete DNS records (A, AAAA, CNAME, TXT, etc.)
- **Load Balancer Pools**: Manage origin pools with health checks
- **Load Balancers**: Configure geo-routing and traffic steering
- **Health Monitors**: Set up health checks for origins
- **Helper Functions**: Convenient methods for common operations

## Usage

### Initialize Client

```go
import "frameworks/pkg/cloudflare"

client := cloudflare.NewClient(
    "your-api-token",
    "zone-id",
    "account-id",
)
```

### DNS Records

```go
// Create CNAME
record, err := client.CreateCNAME("tenant1", "play.example.com", false)

// Create A record
record, err := client.CreateARecord("api", "192.0.2.1", true)

// List records
records, err := client.ListDNSRecords("CNAME", "")

// Delete record
err := client.DeleteDNSRecord(recordID)
```

### Load Balancer Pools

```go
// Create health monitor
monitor := cloudflare.Monitor{
    Type:          "https",
    Path:          "/health",
    Port:          443,
    Interval:      30,
    Timeout:       5,
    Retries:       2,
    ExpectedCodes: "200",
}
createdMonitor, err := client.CreateMonitor(monitor)

// Create pool
pool := cloudflare.Pool{
    Name:        "us-east-pool",
    Description: "US East origins",
    Enabled:     true,
    Monitor:     createdMonitor.ID,
    Origins: []cloudflare.Origin{
        {
            Name:    "us-east-1",
            Address: "192.0.2.10",
            Enabled: true,
            Weight:  1.0,
        },
    },
}
createdPool, err := client.CreatePool(pool)

// Add origin to existing pool
origin := cloudflare.Origin{
    Name:    "us-east-2",
    Address: "192.0.2.11",
    Enabled: true,
    Weight:  1.0,
}
updatedPool, err := client.AddOriginToPool(poolID, origin)

// Remove origin
updatedPool, err := client.RemoveOriginFromPool(poolID, "192.0.2.11")
```

### Load Balancers (Geo-Routing)

```go
// Create geo-routed load balancer
lb := cloudflare.LoadBalancer{
    Name:         "play.example.com",
    Description:  "Playback geo-routing",
    TTL:          60,
    FallbackPool: fallbackPoolID,
    DefaultPools: []string{defaultPoolID},
    RegionPools: map[string][]string{
        cloudflare.RegionWNAM: {usWestPoolID},
        cloudflare.RegionENAM: {usEastPoolID},
        cloudflare.RegionWEU:  {euPoolID},
        cloudflare.RegionEASIA: {apacPoolID},
    },
    Proxied:        false,
    Enabled:        true,
    SteeringPolicy: "geo",
}
createdLB, err := client.CreateLoadBalancer(lb)
```

## Environment Variables

The CLI tool expects these environment variables:

```bash
export CLOUDFLARE_API_TOKEN="your-api-token"
export CLOUDFLARE_ZONE_ID="your-zone-id"
export CLOUDFLARE_ACCOUNT_ID="your-account-id"
```

## API Token Permissions

Your CloudFlare API token needs the following permissions:

- **Zone.DNS**: Edit (for DNS records)
- **Account.Load Balancers**: Edit (for pools and load balancers)
- **Zone.Load Balancers**: Edit (for load balancer configuration)

## Geo Regions

CloudFlare supports these geo regions for routing:

| Constant      | Region                |
| ------------- | --------------------- |
| `RegionWNAM`  | Western North America |
| `RegionENAM`  | Eastern North America |
| `RegionWEU`   | Western Europe        |
| `RegionEEU`   | Eastern Europe        |
| `RegionOC`    | Oceania               |
| `RegionME`    | Middle East           |
| `RegionEASIA` | East Asia             |
| `RegionSEAS`  | Southeast Asia        |
| `RegionNEAS`  | Northeast Asia        |

## Error Handling

All methods return CloudFlare API errors with code and message:

```go
monitor, err := client.CreateMonitor(m)
if err != nil {
    // err contains API error details
    log.Fatalf("Failed to create monitor: %v", err)
}
```

## See Also

- [CloudFlare API Documentation](https://developers.cloudflare.com/api/)
- [Load Balancers Guide](https://developers.cloudflare.com/load-balancing/)
- [DNS Records API](https://developers.cloudflare.com/api/operations/dns-records-for-a-zone-list-dns-records)
