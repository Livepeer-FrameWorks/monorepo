package incidents

import (
	"time"
)

// Delivery channels stored in lookout.notification_outbox.channel.
const (
	ChannelEmail   = "email"
	ChannelSlack   = "slack"
	ChannelDiscord = "discord"
	ChannelKafka   = "kafka"
)

// Notification events carried in DeliveryPayload.Event.
const (
	DeliveryEventOpened   = "opened"
	DeliveryEventResolved = "resolved"
)

// OperatorRouter chooses the operator channels that receive a platform-scope
// incident notification of the given severity.
type OperatorRouter interface {
	ChannelsFor(severity string) []string
}

// DeliveryPayload is the outbox row payload. It snapshots the incident at the
// transition so a delivery retried later still describes that transition.
type DeliveryPayload struct {
	IncidentID string     `json:"incident_id"`
	TenantID   string     `json:"tenant_id,omitempty"`
	ClusterID  string     `json:"cluster_id"`
	Region     string     `json:"region,omitempty"`
	Alertname  string     `json:"alertname,omitempty"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title,omitempty"`
	Summary    string     `json:"summary"`
	Event      string     `json:"event"`
	Resolution string     `json:"resolution,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// KafkaIncidentMessage is the lookout.incidents record Skipper consumes.
type KafkaIncidentMessage struct {
	IncidentID string `json:"incident_id"`
	TenantID   string `json:"tenant_id"`
	ClusterID  string `json:"cluster_id"`
	Severity   string `json:"severity"`
	Summary    string `json:"summary"`
}
