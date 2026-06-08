package decklog

import (
	"context"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	SendTrigger(trigger *ipcpb.MistTrigger) error
	SendTriggerContext(ctx context.Context, trigger *ipcpb.MistTrigger) error
	SendLoadBalancing(data *ipcpb.LoadBalancingData) error
	SendClipLifecycle(data *ipcpb.ClipLifecycleData) error
	SendDVRLifecycle(data *ipcpb.DVRLifecycleData) error
	SendVodLifecycle(data *ipcpb.VodLifecycleData) error
	Close() error
	Health(ctx context.Context, service string) (grpc_health_v1.HealthCheckResponse_ServingStatus, error)
	SendAPIRequestBatch(data *ipcpb.APIRequestBatch) error
	SendMessageLifecycle(data *ipcpb.MessageLifecycleData) error
	SendFederationEvent(data *ipcpb.FederationEventData) error
	SendGatewayTelemetry(event *ipcpb.GatewayTelemetryEvent) error
	SendServiceEvent(event *ipcpb.ServiceEvent) error
}

var _ Interface = (*BatchedClient)(nil)
