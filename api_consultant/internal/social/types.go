package social

import "time"

type ContentType string

const (
	ContentPlatformStats ContentType = "platform_stats"
	ContentFederation    ContentType = "federation"
	ContentKnowledge     ContentType = "knowledge"
)

type PostRecord struct {
	ID             string
	ContentType    ContentType
	TweetText      string
	ContextSummary string
	TriggerData    map[string]any
	Status         string
	SentAt         *time.Time
	CreatedAt      time.Time
}

// EventSignal represents a noteworthy event detected by a detector.
type EventSignal struct {
	ContentType ContentType
	Headline    string
	Data        map[string]any
	Score       float64
}
