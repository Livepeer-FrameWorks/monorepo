package notify

import "time"

type Metric struct {
	Name        string
	Value       string
	Unit        string
	Description string
}

type Recommendation struct {
	Title    string
	Detail   string
	Priority string
}

type NotificationPreferences struct {
	Email     *bool
	Websocket *bool
	MCP       *bool
}

type Report struct {
	TenantID        string
	TenantName      string
	RecipientEmail  string
	InvestigationID string
	Summary         string
	Metrics         []Metric
	Recommendations []Recommendation
	ReportURL       string
	GeneratedAt     time.Time
	Preferences     *NotificationPreferences
}
