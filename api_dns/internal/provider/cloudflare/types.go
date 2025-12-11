package cloudflare

// Origin represents a backend server in a load balancer pool
type Origin struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Enabled bool    `json:"enabled"`
	Weight  float64 `json:"weight,omitempty"`
	Header  *Header `json:"header,omitempty"`
}

// Header contains custom headers to send to origins
type Header struct {
	Host []string `json:"Host,omitempty"`
}

// HealthCheck defines health check configuration for a pool
type HealthCheck struct {
	Path            string `json:"path"`
	Port            int    `json:"port,omitempty"`
	Interval        int    `json:"interval,omitempty"`       // seconds
	Timeout         int    `json:"timeout,omitempty"`        // seconds
	Retries         int    `json:"retries,omitempty"`        // number of consecutive failures
	ExpectedCodes   string `json:"expected_codes,omitempty"` // e.g. "200" or "200-299"
	ExpectedBody    string `json:"expected_body,omitempty"`  // substring to look for
	Method          string `json:"method,omitempty"`         // GET, HEAD, etc.
	Type            string `json:"type,omitempty"`           // http, https, tcp
	FollowRedirects bool   `json:"follow_redirects,omitempty"`
	AllowInsecure   bool   `json:"allow_insecure,omitempty"`
}

// Pool represents a CloudFlare load balancer pool (group of origins)
type Pool struct {
	ID                string   `json:"id,omitempty"`
	Name              string   `json:"name"`
	Description       string   `json:"description,omitempty"`
	Enabled           bool     `json:"enabled"`
	MinimumOrigins    int      `json:"minimum_origins,omitempty"`
	Monitor           string   `json:"monitor,omitempty"`       // Health check monitor ID
	CheckRegions      []string `json:"check_regions,omitempty"` // Regions to run health checks from
	NotificationEmail string   `json:"notification_email,omitempty"`
	Origins           []Origin `json:"origins"`
	Latitude          *float64 `json:"latitude,omitempty"`
	Longitude         *float64 `json:"longitude,omitempty"`
}

// LoadBalancer represents a CloudFlare load balancer (geo-routing configuration)
type LoadBalancer struct {
	ID                 string              `json:"id,omitempty"`
	Name               string              `json:"name"`
	Description        string              `json:"description,omitempty"`
	TTL                int                 `json:"ttl,omitempty"`
	FallbackPool       string              `json:"fallback_pool"`
	DefaultPools       []string            `json:"default_pools"`
	RegionPools        map[string][]string `json:"region_pools,omitempty"`
	CountryPools       map[string][]string `json:"country_pools,omitempty"`
	PopPools           map[string][]string `json:"pop_pools,omitempty"`
	Proxied            bool                `json:"proxied"`
	Enabled            bool                `json:"enabled"`
	SessionAffinity    string              `json:"session_affinity,omitempty"` // "none", "cookie", "ip_cookie"
	SessionAffinityTTL int                 `json:"session_affinity_ttl,omitempty"`
	SteeringPolicy     string              `json:"steering_policy,omitempty"` // "off", "geo", "random", "dynamic_latency"
}

// Monitor represents a health check monitor
type Monitor struct {
	ID              string  `json:"id,omitempty"`
	Type            string  `json:"type"` // http, https, tcp, udp_icmp, icmp_ping, smtp
	Description     string  `json:"description,omitempty"`
	Method          string  `json:"method,omitempty"` // GET, HEAD for HTTP/HTTPS
	Path            string  `json:"path,omitempty"`
	Header          *Header `json:"header,omitempty"`
	Port            int     `json:"port,omitempty"`
	Timeout         int     `json:"timeout,omitempty"`
	Retries         int     `json:"retries,omitempty"`
	Interval        int     `json:"interval,omitempty"`
	ExpectedCodes   string  `json:"expected_codes,omitempty"`
	ExpectedBody    string  `json:"expected_body,omitempty"`
	FollowRedirects bool    `json:"follow_redirects,omitempty"`
	AllowInsecure   bool    `json:"allow_insecure,omitempty"`
	ProbeZone       string  `json:"probe_zone,omitempty"`
}

// DNSRecord represents a DNS record
type DNSRecord struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type"`          // A, AAAA, CNAME, TXT, MX, etc.
	Name     string                 `json:"name"`          // subdomain or @
	Content  string                 `json:"content"`       // IP address, hostname, or value
	TTL      int                    `json:"ttl,omitempty"` // 1 = automatic, otherwise seconds
	Proxied  bool                   `json:"proxied,omitempty"`
	Priority *int                   `json:"priority,omitempty"` // For MX records
	Data     map[string]interface{} `json:"data,omitempty"`     // For SRV, CAA, etc.
	Comment  string                 `json:"comment,omitempty"`
}

// APIResponse is the generic CloudFlare API response wrapper
type APIResponse struct {
	Success    bool         `json:"success"`
	Errors     []APIError   `json:"errors,omitempty"`
	Messages   []APIMessage `json:"messages,omitempty"`
	Result     interface{}  `json:"result,omitempty"`
	ResultInfo *ResultInfo  `json:"result_info,omitempty"`
}

// APIError represents a CloudFlare API error
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// APIMessage represents an informational message
type APIMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ResultInfo contains pagination and result metadata
type ResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

// GeoRegion constants for CloudFlare geo-routing
const (
	RegionWNAM  = "WNAM"  // Western North America
	RegionENAM  = "ENAM"  // Eastern North America
	RegionWEU   = "WEU"   // Western Europe
	RegionEEU   = "EEU"   // Eastern Europe
	RegionNSAM  = "NSAM"  // Northern South America
	RegionSSAM  = "SSAM"  // Southern South America
	RegionOC    = "OC"    // Oceania
	RegionME    = "ME"    // Middle East
	RegionNAF   = "NAF"   // Northern Africa
	RegionSAF   = "SAF"   // Southern Africa
	RegionIN    = "IN"    // India
	RegionSEAS  = "SEAS"  // Southeast Asia
	RegionNEAS  = "NEAS"  // Northeast Asia
	RegionEASIA = "EASIA" // East Asia
)
