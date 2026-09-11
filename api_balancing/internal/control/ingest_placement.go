package control

import (
	"context"
	"errors"
	"math"
	"time"

	"frameworks/api_balancing/internal/balancer"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// IngestPlacementRequest contains validated publisher identity, never its key.
// The preparer must derive policy and current ownership independently.
type IngestPlacementRequest struct {
	TenantID, StreamID, InternalName, Protocol string
	Location                                   *placement.Coordinates
}

type IngestPlacementPreparer interface {
	PrepareIngest(context.Context, IngestPlacementRequest) (balancer.PlacementPreparationResult, error)
}

func resolvePreparedIngestEndpoints(ctx context.Context, deps *IngestDependencies, stream *commodorepb.ResolveStreamContextResponse, key string) (*sharedpb.IngestEndpointResponse, error) {
	if deps.Placement == nil {
		return nil, errors.New("ingest placement is required but unavailable")
	}
	if denial := EvaluateIngestAdmission(stream); denial != nil {
		return nil, denial
	}
	if stream.GetStreamId() == "" || stream.GetInternalName() == "" || stream.GetIngestMode() != "push" {
		return nil, errors.New("ingest placement requires exact push-stream identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request := IngestPlacementRequest{TenantID: stream.TenantId, StreamID: stream.StreamId, InternalName: stream.InternalName}
	if !math.IsNaN(deps.GeoLat) && !math.IsNaN(deps.GeoLon) && !math.IsInf(deps.GeoLat, 0) && !math.IsInf(deps.GeoLon, 0) && math.Abs(deps.GeoLat) <= 90 && math.Abs(deps.GeoLon) <= 180 {
		request.Location = &placement.Coordinates{Latitude: deps.GeoLat, Longitude: deps.GeoLon}
	}
	protocols := []string{"whip", "rtmp", "srt"}
	if deps.Protocol != "" {
		protocols = []string{deps.Protocol}
	}
	var endpoints []*sharedpb.IngestEndpoint
	var expiresAt time.Time
	for _, protocol := range protocols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request.Protocol = protocol
		prepared, err := deps.Placement.PrepareIngest(ctx, request)
		// Unqualified discovery may omit a protocol with no permitted destination.
		// Unknown/ambiguous preparation errors are not evidence for trying elsewhere.
		if errors.Is(err, balancer.ErrPlacementUnavailable) && deps.Protocol == "" {
			continue
		}
		if err != nil {
			return nil, err
		}
		issued, err := placement.PreparationIssuedAt(prepared.AttemptID)
		if err != nil || prepared.Outcome != balancer.PlacementAccepted || prepared.TenantID != request.TenantID ||
			prepared.ObjectID != sharedauthority.LiveStreamAuthorityID(request.StreamID) || prepared.SourceGeneration != "" ||
			prepared.ClusterID == "" || prepared.NodeID == "" || prepared.Protocol != protocol || prepared.Ready ||
			!time.Now().Before(prepared.ExpiresAt) || prepared.ExpiresAt.After(issued.Add(placement.PreparationLifetime)) ||
			issued.After(time.Now().Add(placement.PreparationClockSkew)) || prepared.PublicBaseURL == "" || mist.IngestPublicOrigin(prepared.PublicBaseURL) != prepared.PublicBaseURL {
			return nil, errors.New("ingest preparation does not match the publisher request")
		}
		bound := mist.BindIngestEndpointTemplate(prepared.Endpoint, protocol, key)
		if bound == "" {
			return nil, errors.New("ingest preparation has no usable publishing template")
		}
		if expiresAt.IsZero() || prepared.ExpiresAt.Before(expiresAt) {
			expiresAt = prepared.ExpiresAt
		}
		var endpoint *sharedpb.IngestEndpoint
		for _, existing := range endpoints {
			if existing.NodeId == prepared.NodeID && existing.ClusterId == prepared.ClusterID {
				if existing.BaseUrl != prepared.PublicBaseURL {
					return nil, errors.New("ingest destination changed between protocol preparations")
				}
				endpoint = existing
			}
		}
		if endpoint == nil {
			endpoint = &sharedpb.IngestEndpoint{NodeId: prepared.NodeID, ClusterId: prepared.ClusterID, BaseUrl: prepared.PublicBaseURL,
				Kind: sharedpb.IngestEndpointKind_INGEST_ENDPOINT_KIND_NODE_SPECIFIC}
			endpoints = append(endpoints, endpoint)
		}
		switch protocol {
		case "whip":
			endpoint.WhipUrl = &bound
		case "rtmp":
			endpoint.RtmpUrl = &bound
		case "srt":
			endpoint.SrtUrl = &bound
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(endpoints) == 0 || !time.Now().Before(expiresAt) {
		return nil, balancer.ErrPlacementUnavailable
	}
	// Every URL is independently prepared. No unprepared ranked alternatives are
	// emitted as fallbacks; different protocols may choose different exact nodes.
	return &sharedpb.IngestEndpointResponse{Primary: endpoints[0], Fallbacks: endpoints[1:], Metadata: &sharedpb.IngestMetadata{
		StreamId: stream.StreamId, TenantId: stream.TenantId, StreamKey: key, RecordingEnabled: stream.IsRecordingEnabled,
	}}, nil
}
