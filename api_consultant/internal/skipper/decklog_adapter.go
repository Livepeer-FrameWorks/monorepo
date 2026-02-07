package skipper

import (
	"context"
	"time"

	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// DecklogUsageLogger implements UsageLogger by publishing service events
// to Decklog for the FrameWorks billing/metering pipeline.
type DecklogUsageLogger struct {
	Client *decklog.BatchedClient
	Logger logging.Logger
}

func (d *DecklogUsageLogger) LogChatUsage(ctx context.Context, event ChatUsageEvent) {
	if d.Client == nil || event.TenantID == "" {
		return
	}
	duration := uint64(time.Since(event.StartedAt).Milliseconds())
	totalTokens := uint32(event.TokensIn + event.TokensOut)
	agg := &pb.APIRequestAggregate{
		TenantId:        event.TenantID,
		AuthType:        resolveAuthType(ctx),
		OperationType:   "skipper_chat",
		OperationName:   "skipper_chat",
		RequestCount:    1,
		ErrorCount:      boolToCount(event.HadError),
		TotalDurationMs: duration,
		TotalComplexity: totalTokens,
		Timestamp:       event.StartedAt.Unix(),
	}
	batch := &pb.APIRequestBatch{
		Timestamp:  time.Now().Unix(),
		SourceNode: "skipper",
		Aggregates: []*pb.APIRequestAggregate{agg},
	}
	svcEvent := &pb.ServiceEvent{
		EventType: "api_request_batch",
		Timestamp: timestamppb.Now(),
		Source:    "skipper",
		TenantId:  event.TenantID,
		UserId:    event.UserID,
		Payload:   &pb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
	}
	if err := d.Client.SendServiceEvent(svcEvent); err != nil && d.Logger != nil {
		d.Logger.WithError(err).Warn("Failed to emit Skipper usage event")
	}
}

func resolveAuthType(ctx context.Context) string {
	if authType := GetAuthType(ctx); authType != "" {
		return authType
	}
	if GetJWTToken(ctx) != "" {
		return "jwt"
	}
	return "unknown"
}

func boolToCount(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}
