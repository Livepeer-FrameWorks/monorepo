package model

import (
	"time"

	pb "frameworks/pkg/proto"
)

// TenantEvent is the unified firehose event type for live subscriptions.
type TenantEvent struct {
	Type      string    `json:"type"`
	Channel   string    `json:"channel"`
	Timestamp time.Time `json:"timestamp"`

	StreamEvent       *StreamEvent               `json:"streamEvent"`
	ViewerMetrics     *pb.ClientLifecycleUpdate  `json:"viewerMetrics"`
	ConnectionEvent   *pb.ConnectionEvent        `json:"connectionEvent"`
	TrackListUpdate   *pb.StreamTrackListTrigger `json:"trackListUpdate"`
	ClipLifecycle     *pb.ClipLifecycleData      `json:"clipLifecycle"`
	DvrEvent          *pb.DVRLifecycleData       `json:"dvrEvent"`
	VodLifecycle      *pb.VodLifecycleData       `json:"vodLifecycle"`
	StorageEvent      *pb.StorageEvent           `json:"storageEvent"`
	StorageSnapshot   *pb.StorageSnapshot        `json:"storageSnapshot"`
	ProcessingEvent   *pb.ProcessingUsageRecord  `json:"processingEvent"`
	RoutingEvent      *pb.RoutingEvent           `json:"routingEvent"`
	SystemHealthEvent *pb.NodeLifecycleUpdate    `json:"systemHealthEvent"`
}
