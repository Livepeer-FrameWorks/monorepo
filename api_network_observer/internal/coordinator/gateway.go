package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"frameworks/api_network_observer/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/encoding/protojson"
	"strings"
	"time"
)

func (a *API) Gateway(ctx context.Context, message kafka.Message) error {
	var event kafka.AnalyticsEvent
	if err := json.Unmarshal(message.Value, &event); err != nil {
		return err
	}
	// Customer payloads remain in the analytics pipeline, outside coordinator state.
	if event.EventType != "orchestrator_discovery_observed" && event.EventType != "orchestrator_state_update" && event.EventType != "orchestrator_observer_report" {
		return nil
	}
	data := event.Data
	// Prober observations already update inventory through the signed ingest path.
	if data["producer_kind"] != "gateway" {
		return nil
	}
	if fmt.Sprint(data["cluster_owner_tenant_id"]) != a.Store.TenantID || fmt.Sprint(data["network_id"]) != a.Engine.Network || fmt.Sprint(data["environment"]) != a.Engine.Environment {
		return nil
	}
	if time.Since(event.Timestamp) > 30*time.Minute || event.Timestamp.After(time.Now().Add(time.Minute)) {
		return nil
	}
	fields := map[string]any{}
	for k, v := range data {
		switch k {
		case "gateway_id", "gateway_region", "cluster_id", "cluster_owner_tenant_id", "stream_tenant_id", "network_id", "environment", "producer_kind":
		default:
			fields[k] = v
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var discovery pb.OrchestratorDiscoveryObserved
	var state pb.OrchestratorStateUpdate
	var wallet, uri, ip string
	var success bool
	var check obs.Check
	options := protojson.UnmarshalOptions{DiscardUnknown: true}
	if event.EventType == "orchestrator_observer_report" {
		var report pb.OrchestratorObserverReport
		if err = options.Unmarshal(raw, &report); err != nil {
			return err
		}
		if !report.Healthy || fmt.Sprint(data["producer_kind"]) != "gateway" {
			return nil
		}
		return a.Store.Update(ctx, func(s *store.State, _ *sql.Tx) error {
			id := "gateway:" + fmt.Sprint(data["gateway_id"])
			o := s.Observers[id]
			o.ID = id
			o.Kind = "gateway"
			o.Region = fmt.Sprint(data["gateway_region"])
			if event.Timestamp.After(o.LastSeen) {
				o.LastSeen = event.Timestamp
			}
			s.Observers[id] = o
			return nil
		})
	}
	if event.EventType == "orchestrator_discovery_observed" {
		if err = options.Unmarshal(raw, &discovery); err != nil {
			return err
		}
		wallet, uri = discovery.OrchAddr, discovery.OrchUrl
		if discovery.Vantage == nil || !discovery.Vantage.Dialed || discovery.Vantage.ResolvedIp == "" {
			return nil
		}
		ip = discovery.Vantage.ResolvedIp
		success = discovery.Reachable
		check = obs.Check{ObservedAt: event.Timestamp, Success: success, Qualifying: obs.DiscoveryQualifies(success, discovery.ObserverHealthy, discovery.FailureKind), ActualIP: ip, LatencyMS: discovery.DiscoveryLatencyMs, FailureKind: discovery.FailureKind}
	} else {
		if err = options.Unmarshal(raw, &state); err != nil {
			return err
		}
		wallet, uri = state.OrchAddr, state.CanonicalUrl
		if state.Vantage == nil || !state.Vantage.Dialed || state.Vantage.ResolvedIp == "" {
			return nil
		}
		ip = state.Vantage.ResolvedIp
		success = true
		check = obs.Check{ObservedAt: event.Timestamp, Success: true, Qualifying: true, ActualIP: ip}
	}
	return a.Store.Update(ctx, func(s *store.State, _ *sql.Tx) error {
		var resource obs.Resource
		found := false
		for _, r := range s.Resources {
			if strings.EqualFold(r.ID, wallet) && r.Active {
				resource = r
				found = true
				if r.URI == uri {
					break
				}
			}
		}
		if !found {
			return nil
		}
		resource.URI = uri
		endpoint, endpointErr := obs.NewEndpoint(resource, ip, event.Timestamp)
		if endpointErr != nil {
			return fmt.Errorf("invalid observed gateway endpoint: %w", endpointErr)
		}
		// An observed alternate endpoint belongs to the same verified onchain identity.
		if _, ok := s.Resources[resource.Key()]; !ok {
			resource.CatalogSource = "advertised"
			s.Resources[resource.Key()] = resource
		}
		a.Engine.endpoint(s, resource, ip, event.Timestamp)
		p := s.Endpoints[endpoint.ID]
		if event.EventType == "orchestrator_state_update" && !event.Timestamp.Before(p.LastCheck) {
			p.Capabilities = state.Capabilities
			p.Hardware = state.Hardware
			if state.PriceKnown && state.PixelsPerUnit > 0 {
				p.Price = &obs.Quote{Numerator: fmt.Sprint(state.PricePerUnit), Denominator: fmt.Sprint(state.PixelsPerUnit), Currency: state.PriceCurrency, Unit: state.PriceUnit, ObservedAt: event.Timestamp}
			}
		}
		observerID := "gateway:" + fmt.Sprint(data["gateway_id"])
		check.ObserverID = observerID
		o := s.Observers[observerID]
		o.ID = observerID
		o.Kind = "gateway"
		o.Region = fmt.Sprint(data["gateway_region"])
		if event.Timestamp.After(o.LastSeen) {
			o.LastSeen = event.Timestamp
		}
		s.Observers[observerID] = o
		{
			if event.Timestamp.After(p.LastCheck) {
				p.LastCheck = event.Timestamp
			}
			if success && event.Timestamp.After(p.LastSuccess) {
				p.LastSuccess = event.Timestamp
				if !event.Timestamp.Before(p.LastCheck) {
					p.FailureStreak = 0
				}
			}
			updated := false
			for i, c := range p.Checks {
				if c.ObserverID == observerID {
					if !check.ObservedAt.Before(c.ObservedAt) {
						p.Checks[i] = check
					}
					updated = true
					break
				}
			}
			if !updated {
				p.Checks = append(p.Checks, check)
			}
			s.Endpoints[p.ID] = p
		}
		return nil
	})
}
