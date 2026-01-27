package model

import "time"

// StreamEvent is a canonical analytics event (historical + live).
type StreamEvent struct {
	Id        string            `json:"id"`
	EventId   string            `json:"eventId"`
	StreamId  string            `json:"streamId"`
	NodeId    *string           `json:"nodeId"`
	Type      StreamEventType   `json:"type"`
	Status    *StreamStatus     `json:"status"`
	Timestamp time.Time         `json:"timestamp"`
	Details   *string           `json:"details"`
	Payload   *string           `json:"payload"`
	Source    StreamEventSource `json:"source"`

	BufferState     *string  `json:"bufferState"`
	HasIssues       *bool    `json:"hasIssues"`
	TrackCount      *int     `json:"trackCount"`
	QualityTier     *string  `json:"qualityTier"`
	PrimaryWidth    *int     `json:"primaryWidth"`
	PrimaryHeight   *int     `json:"primaryHeight"`
	PrimaryFps      *float64 `json:"primaryFps"`
	PrimaryCodec    *string  `json:"primaryCodec"`
	PrimaryBitrate  *int     `json:"primaryBitrate"`
	DownloadedBytes *float64 `json:"downloadedBytes"`
	UploadedBytes   *float64 `json:"uploadedBytes"`
	TotalViewers    *int     `json:"totalViewers"`
	TotalInputs     *int     `json:"totalInputs"`
	TotalOutputs    *int     `json:"totalOutputs"`
	ViewerSeconds   *float64 `json:"viewerSeconds"`

	RequestUrl  *string  `json:"requestUrl"`
	Protocol    *string  `json:"protocol"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Location    *string  `json:"location"`
	CountryCode *string  `json:"countryCode"`
	City        *string  `json:"city"`
}
