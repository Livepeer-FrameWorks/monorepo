package bunny

type Zone struct {
	ID                       int64    `json:"Id"`
	Domain                   string   `json:"Domain"`
	Records                  []Record `json:"Records,omitempty"`
	Nameserver1              string   `json:"Nameserver1"`
	Nameserver2              string   `json:"Nameserver2"`
	CustomNameserversEnabled bool     `json:"CustomNameserversEnabled"`
}

func (z Zone) Nameservers() []string {
	out := make([]string, 0, 2)
	if z.Nameserver1 != "" {
		out = append(out, z.Nameserver1)
	}
	if z.Nameserver2 != "" && z.Nameserver2 != z.Nameserver1 {
		out = append(out, z.Nameserver2)
	}
	return out
}

type Record struct {
	ID                    int64    `json:"Id,omitempty"`
	Type                  int      `json:"Type"`
	TTL                   int      `json:"Ttl,omitempty"`
	Value                 string   `json:"Value,omitempty"`
	Name                  string   `json:"Name,omitempty"`
	Weight                int      `json:"Weight,omitempty"`
	Priority              int      `json:"Priority,omitempty"`
	Port                  int      `json:"Port,omitempty"`
	Flags                 int      `json:"Flags,omitempty"`
	Tag                   string   `json:"Tag,omitempty"`
	MonitorType           int      `json:"MonitorType,omitempty"`
	MonitorStatus         int      `json:"MonitorStatus,omitempty"`
	GeolocationLatitude   *float64 `json:"GeolocationLatitude,omitempty"`
	GeolocationLongitude  *float64 `json:"GeolocationLongitude,omitempty"`
	LatencyZone           string   `json:"LatencyZone,omitempty"`
	SmartRoutingType      int      `json:"SmartRoutingType,omitempty"`
	Disabled              bool     `json:"Disabled,omitempty"`
	Comment               string   `json:"Comment,omitempty"`
	AutoSSLIssuance       bool     `json:"AutoSslIssuance,omitempty"`
	Accelerated           bool     `json:"Accelerated,omitempty"`
	AcceleratedPullZoneID int64    `json:"AcceleratedPullZoneId,omitempty"`
	AccelerationStatus    int      `json:"AccelerationStatus,omitempty"`
	EnviromentalVariables []EnvVar `json:"EnviromentalVariables,omitempty"` // Bunny API spelling.
}

type EnvVar struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type listZonesResponse struct {
	Items        []Zone `json:"Items"`
	CurrentPage  int    `json:"CurrentPage"`
	TotalItems   int    `json:"TotalItems"`
	HasMoreItems bool   `json:"HasMoreItems"`
}

const (
	RecordTypeA     = 0
	RecordTypeAAAA  = 1
	RecordTypeCNAME = 2
	RecordTypeTXT   = 3
	RecordTypeNS    = 12

	MonitorTypeNone = 0

	SmartRoutingNone        = 0
	SmartRoutingLatency     = 1
	SmartRoutingGeolocation = 2
)
