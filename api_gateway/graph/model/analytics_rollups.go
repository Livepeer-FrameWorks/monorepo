package model

// Rollup analytics types — hand-written to prevent gqlgen autobind
// from matching same-named proto types (proto types have different field
// shapes and are used for gRPC transport, not GraphQL responses).

type RoutingEfficiency struct {
	TotalDecisions     int                   `json:"totalDecisions"`
	SuccessCount       int                   `json:"successCount"`
	SuccessRate        float64               `json:"successRate"`
	AvgRoutingDistance float64               `json:"avgRoutingDistance"`
	AvgLatencyMs       float64               `json:"avgLatencyMs"`
	TopCountries       []*RoutingCountryStat `json:"topCountries"`
}

type RoutingCountryStat struct {
	CountryCode  string `json:"countryCode"`
	RequestCount int    `json:"requestCount"`
}

type StreamHealthSummary struct {
	AvgBitrate         float64 `json:"avgBitrate"`
	AvgFps             float64 `json:"avgFps"`
	AvgBufferHealth    float64 `json:"avgBufferHealth"`
	TotalRebufferCount int     `json:"totalRebufferCount"`
	TotalIssueCount    int     `json:"totalIssueCount"`
	SampleCount        int     `json:"sampleCount"`
	HasActiveIssues    bool    `json:"hasActiveIssues"`
	CurrentQualityTier *string `json:"currentQualityTier,omitempty"`
}

type ClientQoeSummary struct {
	AvgPacketLossRate   *float64 `json:"avgPacketLossRate,omitempty"`
	PeakPacketLossRate  *float64 `json:"peakPacketLossRate,omitempty"`
	AvgBandwidthIn      float64  `json:"avgBandwidthIn"`
	AvgBandwidthOut     float64  `json:"avgBandwidthOut"`
	AvgConnectionTime   float64  `json:"avgConnectionTime"`
	TotalActiveSessions int      `json:"totalActiveSessions"`
}
