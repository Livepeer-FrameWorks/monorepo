package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func (h *AnalyticsHandler) processNetworkObservation(ctx context.Context, event kafka.AnalyticsEvent) error {
	id, err := uuid.Parse(event.EventID)
	if err != nil {
		return fmt.Errorf("network observation requires stable event ID: %w", err)
	}
	owner, err := uuid.Parse(fmt.Sprint(event.Data["cluster_owner_tenant_id"]))
	if err != nil {
		return fmt.Errorf("invalid network observation owner")
	}
	m := obs.Metric{EventID: id.String(), Timestamp: event.Timestamp, Provider: "livepeer", Network: fmt.Sprint(event.Data["network_id"]), Environment: fmt.Sprint(event.Data["environment"]), ObserverID: fmt.Sprint(event.Data["gateway_id"]), Region: fmt.Sprint(event.Data["gateway_region"]), ProducerKind: fmt.Sprint(event.Data["producer_kind"]), Workload: "transcoding", Qualifying: true}
	if m.Network == "" || m.Environment == "" || m.Network == "<nil>" || m.Environment == "<nil>" {
		return fmt.Errorf("network namespace required")
	}
	var payload proto.Message
	switch event.EventType {
	case "orchestrator_discovery_observed":
		payload = &pb.OrchestratorDiscoveryObserved{}
	case "orchestrator_state_update":
		payload = &pb.OrchestratorStateUpdate{}
	case "orchestrator_transcode_outcome":
		payload = &pb.OrchestratorTranscodeOutcome{}
	case "orchestrator_candidate_decision":
		payload = &pb.OrchestratorCandidateDecision{}
	case "orchestrator_segment_result":
		payload = &pb.OrchestratorSegmentResult{}
	case "orchestrator_observer_report":
		payload = &pb.OrchestratorObserverReport{}
	default:
		return nil
	}
	if err = h.parseProtobufData(event, payload); err != nil {
		return err
	}
	private := map[string]any{}
	privateTenant := uuid.Nil
	if value, ok := event.Data["stream_tenant_id"]; ok {
		if privateTenant, err = uuid.Parse(fmt.Sprint(value)); err != nil {
			return fmt.Errorf("invalid stream scope")
		}
	}
	switch p := payload.(type) {
	case *pb.OrchestratorDiscoveryObserved:
		m.Kind = "discovery"
		m.ResourceID = p.OrchAddr
		m.URI = p.OrchUrl
		m.Success = p.Reachable
		m.LatencyMS = p.DiscoveryLatencyMs
		m.FailureKind = p.FailureKind
		m.CadenceSeconds = p.ScheduledIntervalSeconds
		if p.Vantage != nil && p.Vantage.Dialed {
			m.IP = p.Vantage.ResolvedIp
		}
		if m.IP == "" {
			m.IP = p.TargetIp
		}
		m.Qualifying = p.ObserverHealthy && (m.Success || p.FailureKind != "observer_cancelled" && p.FailureKind != "observer_configuration" && p.FailureKind != "ambiguous_attribution")
	case *pb.OrchestratorStateUpdate:
		m.Kind = "state"
		m.ResourceID = p.OrchAddr
		m.URI = p.CanonicalUrl
		m.Success = true
		if p.Vantage != nil && p.Vantage.Dialed {
			m.IP = p.Vantage.ResolvedIp
		}
		if p.PriceKnown && p.PixelsPerUnit > 0 {
			m.Price = &obs.Quote{Numerator: strconv.FormatInt(p.PricePerUnit, 10), Denominator: strconv.FormatInt(p.PixelsPerUnit, 10), Currency: p.PriceCurrency, Unit: p.PriceUnit, ObservedAt: event.Timestamp}
		}
	case *pb.OrchestratorTranscodeOutcome:
		m.Kind = "attempt"
		m.ResourceID = p.OrchAddr
		m.URI = p.OrchUrl
		if p.RemoteIpVerified {
			m.IP = p.ResolvedIp
		}
		m.Success = p.Success
		m.LatencyMS = p.OverallMs
		m.SourceDurationMS = p.SourceDurationMs
		m.ResponseHeadersMS = p.ResponseHeadersMs
		m.ResponseBodyMS = p.ResponseBodyMs
		m.ProfileKey = obs.ProfileKey(p.Profiles)
		m.FailureKind = p.ErrorKind
		m.Disposition = p.Disposition
		m.Qualifying = p.Disposition != "input_fault" && p.Disposition != "hedge_loser" && p.Disposition != "local_cancelled"
		private["session_id"] = p.SessionId
		private["manifest_id_hash"] = p.ManifestIdHash
		private["seq_no"] = p.SeqNo
	case *pb.OrchestratorCandidateDecision:
		m.Kind = "decision"
		m.ResourceID = p.OrchAddr
		m.URI = p.OrchUrl
		m.IP = p.ResolvedIp
		m.Disposition = p.Disposition
		m.Success = p.Disposition == "accepted" || p.Disposition == "selected"
		m.Qualifying = false
		m.FailureKind = p.Reason
		m.ProfileKey = p.CapabilityKey
		m.LatencyMS = p.ElapsedMs
		private["stage"] = p.Stage
		private["usable_candidates"] = p.UsableCandidates
	case *pb.OrchestratorSegmentResult:
		m.Kind = "segment"
		m.ResourceID = p.OrchAddr
		m.URI = p.OrchUrl
		m.IP = p.ResolvedIp
		m.Success = p.Success
		m.LatencyMS = p.OverallMs
		m.SourceDurationMS = p.SourceDurationMs
		m.Attempts = p.Attempts
		m.FallbackCount = p.FallbackCount
		m.DeadlineExceeded = p.DeadlineExceeded
		m.FailureKind = p.ErrorKind
		m.Qualifying = p.ErrorKind != "input_fault" && p.ErrorKind != "canceled" && p.ErrorKind != "local_cancelled"
		m.ProfileKey = obs.ProfileKey(p.Profiles)
		private["session_id"] = p.SessionId
		private["manifest_id_hash"] = p.ManifestIdHash
		private["seq_no"] = p.SeqNo
		private["replacement_setup_ms"] = p.ReplacementSetupMs
		private["delivery_session_id"] = p.DeliverySessionId
		private["downstream_status"] = p.DownstreamStatus
	case *pb.OrchestratorObserverReport:
		m.Kind = "coverage"
		m.Success = p.Healthy
		m.CadenceSeconds = p.CadenceSeconds
		private["expected"] = p.Expected
		private["completed"] = p.Completed
		private["dropped_records"] = p.DroppedRecords
		private["queue_records"] = p.QueueRecords
		private["failed"] = p.Failed
		private["report_kind"] = p.ReportKind
	}
	// Gateway topology and customer correlation never enter the public projection.
	if privateTenant != uuid.Nil {
		private["stream_tenant_id"] = privateTenant.String()
	}
	m.ResourceID = strings.ToLower(m.ResourceID)
	public := m
	public.ObserverID = ""
	publicRaw, err := json.Marshal(public)
	if err != nil {
		return err
	}
	privateRaw, err := json.Marshal(private)
	if err != nil {
		return err
	}
	batch, err := periscopeingestdb.PrepareNetworkObservation(ctx, h.clickhouse)
	if err != nil {
		return err
	}

	defer batch.Close()
	if err = batch.Append(periscopeingestdb.NetworkObservationRow{TenantID: owner, EventID: id, Timestamp: m.Timestamp, Metric: m, PublicData: string(publicRaw), PrivateTenantID: privateTenant, PrivateData: string(privateRaw)}); err != nil {
		return err
	}
	if err := batch.Send(); err != nil {
		return err
	}
	h.networkScopes.Store(networkScope{Tenant: owner.String(), Network: m.Network, Environment: m.Environment}, true)
	// Preserve gateway current-state readers without replaying additive legacy samples.
	if m.ProducerKind == "gateway" {
		switch p := payload.(type) {
		case *pb.OrchestratorStateUpdate:
			return h.processOrchestratorStateUpdate(ctx, event)
		case *pb.OrchestratorDiscoveryObserved:
			if p.Vantage != nil && p.Vantage.Dialed && p.Vantage.ResolvedIp != "" {
				ident, err := requireOrchestratorIdentity(event)
				if err != nil {
					return err
				}
				latency := p.DiscoveryLatencyMs
				if !p.Reachable {
					latency = 0
				}
				return h.upsertOrchestratorVantage(ctx, event, ident, p.OrchAddr, p.Vantage, latency, p.Score, p.Reachable)
			}
		}
	}
	return nil
}
