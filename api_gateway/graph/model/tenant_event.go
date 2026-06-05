package model

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"time"
)

// TenantEvent is the unified firehose event type for live subscriptions.
type TenantEvent struct {
	Type      string    `json:"type"`
	Channel   string    `json:"channel"`
	Timestamp time.Time `json:"timestamp"`

	StreamEvent          *StreamEvent                       `json:"streamEvent"`
	ViewerMetrics        *ipcpb.ClientLifecycleUpdate       `json:"viewerMetrics"`
	ConnectionEvent      *periscopepb.ConnectionEvent       `json:"connectionEvent"`
	TrackListUpdate      *ipcpb.StreamTrackListTrigger      `json:"trackListUpdate"`
	ClipLifecycle        *ipcpb.ClipLifecycleData           `json:"clipLifecycle"`
	DvrEvent             *ipcpb.DVRLifecycleData            `json:"dvrEvent"`
	VodLifecycle         *ipcpb.VodLifecycleData            `json:"vodLifecycle"`
	StorageEvent         *periscopepb.StorageEvent          `json:"storageEvent"`
	StorageSnapshot      *ipcpb.StorageSnapshot             `json:"storageSnapshot"`
	ProcessingEvent      *periscopepb.ProcessingUsageRecord `json:"processingEvent"`
	RoutingEvent         *periscopepb.RoutingEvent          `json:"routingEvent"`
	SystemHealthEvent    *ipcpb.NodeLifecycleUpdate         `json:"systemHealthEvent"`
	SkipperInvestigation *SkipperInvestigationEvent         `json:"skipperInvestigation"`
}

// SkipperInvestigationEvent represents a realtime Skipper investigation notification.
type SkipperInvestigationEvent struct {
	ReportID     string `json:"reportId"`
	ResourceType string `json:"resourceType"`
}
