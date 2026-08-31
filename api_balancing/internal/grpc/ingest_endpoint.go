package grpc

import (
	"context"
	"math"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/triggers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ResolveIngestEndpoint resolves the best ingest node(s) for a stream key.
//
// This is the terminal hop of the GraphQL resolveIngestEndpoint chain
// (gateway → Commodore → here); Commodore's finalizeIngestResponse re-derives
// title/description and strips owner-only metadata for non-owners on the way
// back, so full metadata is returned here.
//
// Like the HTTP front door, admission goes through ResolveStreamContext rather
// than ValidateStreamKey: resolving an endpoint must not claim the ingest
// lease. PUSH_REWRITE is still the only push-ingest caller that claims through
// ValidateStreamKey.
func (s *FoghornGRPCServer) ResolveIngestEndpoint(ctx context.Context, req *sharedpb.IngestEndpointRequest) (*sharedpb.IngestEndpointResponse, error) {
	ctx = control.MediaRequestContext(ctx, "ingest_grpc")
	start := time.Now()

	streamKey := req.GetStreamKey()
	if streamKey == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_key is required")
	}
	var localContext *commodorepb.ResolveStreamContextResponse
	localHandled := false
	if s.localIngestResolver != nil {
		var localErr error
		localContext, localHandled, localErr = s.localIngestResolver.ResolveLocalIngestContext(ctx, streamKey)
		if localErr != nil {
			if triggers.IsLocalAuthorityDenied(localErr) || triggers.IsLocalAuthorityExpired(localErr) {
				return nil, status.Error(codes.Unavailable, "stream validation is unavailable")
			}
			s.logger.WithError(localErr).Warn("Local ingest projection unavailable; using connected validation")
			localContext = nil
			localHandled = false
		}
		if localHandled {
			if denial := control.EvaluateIngestAdmission(localContext); denial != nil {
				return nil, status.Error(denial.GRPCCode, denial.Message)
			}
		}
	}

	// No cluster is declared. Everywhere else Foghorn passes its own CLUSTER_ID
	// it is reporting where a stream landed, on the cluster it runs; here it is
	// asking where a publisher may go, and one process serves many virtual
	// media clusters. Commodore answers with the tenant's authorized, healthy
	// cluster_peers envelope, which selection then ranks nodes across.
	var streamCtx *commodorepb.ResolveStreamContextResponse
	var err error
	if control.CommodoreClient != nil {
		streamCtx, err = control.CommodoreClient.ResolveStreamContextByStreamKey(ctx, streamKey, "")
	}
	if err != nil || streamCtx == nil {
		streamCtx = localContext
		if localHandled && streamCtx != nil && streamCtx.GetActiveIngestClusterId() == "" {
			if owner := strings.TrimSpace(streamCtx.GetOriginClusterId()); owner != "" {
				streamCtx.ActiveIngestClusterId = &owner
			}
		}
	}
	if streamCtx == nil {
		s.logger.WithFields(logging.Fields{
			"error": err,
		}).Warn("ResolveIngestEndpoint: stream context resolution failed")
		return nil, status.Errorf(codes.Unavailable, "stream validation unavailable: %v", err)
	}

	if denial := control.EvaluateIngestAdmission(streamCtx); denial != nil {
		return nil, status.Error(denial.GRPCCode, denial.Message)
	}

	// Default to NaN so a missing GeoIP lookup is not mistaken for (0,0).
	lat, lon := math.NaN(), math.NaN()
	if clientIP := req.GetViewerIp(); clientIP != "" && s.geoipReader != nil {
		if geoData := geoip.LookupCached(ctx, s.geoipReader, s.geoipCache, clientIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	response, err := control.ResolveIngestEndpoints(ctx, &control.IngestDependencies{
		LB:     s.lb,
		GeoLat: lat,
		GeoLon: lon,
	}, streamCtx, streamKey)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":         err,
			"internal_name": streamCtx.GetInternalName(),
		}).Warn("ResolveIngestEndpoint: no ingest-capable nodes")
		return nil, status.Error(codes.Unavailable, "no ingest-capable nodes are available")
	}

	s.emitIngestRoutingEvent(streamCtx.GetInternalName(), streamCtx.GetStreamId(), streamCtx.GetTenantId(),
		req.GetViewerIp(), lat, lon, response, float32(time.Since(start).Milliseconds()))

	return response, nil
}

// emitIngestRoutingEvent records the decision to Decklog. The viewer-side
// emitRoutingEvent takes a ViewerEndpoint, so the RoutingEvent is filled
// directly here rather than reused.
func (s *FoghornGRPCServer) emitIngestRoutingEvent(
	internalName, streamID, streamTenantID, clientIP string,
	lat, lon float64,
	response *sharedpb.IngestEndpointResponse,
	durationMs float32,
) {
	if s.decklogClient == nil || response.GetPrimary() == nil {
		return
	}
	primary := response.GetPrimary()
	// Off the request path via a bounded queue: Decklog's SendLoadBalancing is
	// blocking, so a synchronous send would stall GraphQL ingest resolution and
	// a goroutine-per-event scheme would pile up during an outage. Queue workers
	// impose the send deadline.
	handlers.EnqueueRoutingEvent(s.decklogClient, &handlers.RoutingEvent{
		Status:          "success",
		InternalName:    internalName,
		StreamID:        streamID,
		StreamTenantID:  streamTenantID,
		ClientIP:        clientIP,
		ClientLat:       lat,
		ClientLon:       lon,
		SelectedNodeID:  primary.GetNodeId(),
		Score:           uint64(primary.GetLoadScore()),
		LatencyMs:       durationMs,
		CandidatesCount: int32(1 + len(response.GetFallbacks())),
		EventType:       "ingest_resolve",
		Source:          "grpc",
	})
}
