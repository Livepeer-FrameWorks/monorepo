package sessionrevocations

import (
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

// Decode adapts realtime envelopes for Bridge and Signalman's local event cache.
// Backend projections decode authoritative internal domain events directly.
func Decode(tenant string, event *signalmanpb.TenantEvent) *commodorepb.SessionRevocation {
	if event == nil || event.GetData() == nil {
		return nil
	}
	payload, err := event.GetData().UnmarshalNew()
	if err != nil {
		return nil
	}
	return DecodePayload(tenant, event.GetType(), payload)
}
