package coordinator

import (
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strconv"
)

type encodedEvent struct {
	ID      string
	Payload []byte
}

func (a *API) encodeBatch(s *store.State, b obs.Batch) ([]encodedEvent, error) {
	var result []encodedEvent
	if b.Report != nil {
		v := b.Report
		event := &pb.GatewayTelemetryEvent{EventId: uuid.NewSHA1(uuid.NameSpaceOID, []byte(b.ID+":report")).String(), SchemaVersion: 3, NetworkId: a.Engine.Network, Environment: a.Engine.Environment, ProducerKind: "prober", GatewayId: b.ObserverID, GatewayRegion: s.Observers[b.ObserverID].Region, ClusterId: a.ClusterID, ClusterOwnerTenantId: a.Store.TenantID, Timestamp: timestamppb.New(v.ObservedAt), Payload: &pb.GatewayTelemetryEvent_ObserverReport{ObserverReport: &pb.OrchestratorObserverReport{ReportKind: v.Kind, Expected: v.Expected, Completed: v.Completed, Failed: v.Failed, CadenceSeconds: v.CadenceSeconds, QueueRecords: v.QueueRecords, DroppedRecords: v.DroppedRecords, Healthy: v.Healthy}}}
		raw, marshalErr := proto.Marshal(event)
		if marshalErr != nil {
			return nil, marshalErr
		}
		result = append(result, encodedEvent{event.EventId, raw})
	}
	for _, v := range b.Results {
		p := s.Endpoints[v.EndpointID]
		o := s.Observers[b.ObserverID]
		event := &pb.GatewayTelemetryEvent{EventId: v.ID, SchemaVersion: 3, NetworkId: a.Engine.Network, Environment: a.Engine.Environment, ProducerKind: "prober", GatewayId: b.ObserverID, GatewayRegion: o.Region, ClusterId: a.ClusterID, ClusterOwnerTenantId: a.Store.TenantID, Timestamp: timestamppb.New(v.Check.ObservedAt), Payload: &pb.GatewayTelemetryEvent_Discovery{Discovery: &pb.OrchestratorDiscoveryObserved{OrchAddr: p.Resource.ID, OrchUrl: p.Resource.URI, Vantage: &pb.OrchestratorVantageGeo{ResolvedIp: v.Check.ActualIP, Dialed: v.Check.ActualIP != ""}, TargetIp: p.IP, Reachable: v.Check.Success, DiscoveryLatencyMs: v.Check.LatencyMS, FailureKind: v.Check.FailureKind, AttemptId: v.AssignmentID, SamplingSource: "prober", ScheduledIntervalSeconds: uint32(p.CadenceSeconds), ObserverHealthy: true}}}
		if v.Check.Success {
			price := &pb.OrchestratorStateUpdate{OrchAddr: p.Resource.ID, CanonicalUrl: p.Resource.URI, AdvertisedNodeUrls: v.AdvertisedURIs, Capabilities: v.Capabilities, Hardware: v.Hardware, Source: "prober", Vantage: &pb.OrchestratorVantageGeo{ResolvedIp: v.Check.ActualIP, Dialed: true}}
			if v.Quote != nil {
				num, e1 := strconv.ParseInt(v.Quote.Numerator, 10, 64)
				den, e2 := strconv.ParseInt(v.Quote.Denominator, 10, 64)
				if e1 == nil && e2 == nil && den > 0 {
					price.PriceKnown = true
					price.PricePerUnit = num
					price.PixelsPerUnit = den
					price.PriceCurrency = v.Quote.Currency
					price.PriceUnit = v.Quote.Unit
				}
			}
			stateEvent := proto.CloneOf(event)
			stateEvent.EventId = uuid.NewSHA1(uuid.NameSpaceOID, []byte(v.ID+":state")).String()
			stateEvent.Payload = &pb.GatewayTelemetryEvent_State{State: price}
			stateRaw, e := proto.Marshal(stateEvent)
			if e != nil {
				return nil, e
			}
			result = append(result, encodedEvent{stateEvent.EventId, stateRaw})
		}
		payload, marshalErr := proto.Marshal(event)
		if marshalErr != nil {
			return nil, marshalErr
		}
		result = append(result, encodedEvent{event.EventId, payload})
	}
	return result, nil
}
