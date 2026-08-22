package handlers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

// processOrchestratorDiscoveryObserved writes a per-vantage discovery
// observation to orchestrator_discovery_samples (raw 30d, materialised into
// 5m + 1h rollups for uptime / success-rate / latency analysis). Multi-IP
// rows for the same attempt are intentional: DNS round-robin / geo-anycast
// is a first-class dimension keyed by (gateway, orch_addr, resolved_ip).
//
// tenant_id here is the cluster_owner_tenant_id (these are global/cluster-
// scoped events; no per-stream context). cluster_owner_tenant_id is also
// in event.Data so downstream joins can separate stream tenant from cluster
// owner tenant; the column on the row is the canonical filter.
//
// The ingest path is write-only. First-seen failures without an eth address
// stay under the stable url:<host> key emitted by the gateway; later attempts
// use the eth address once the gateway has learned it.
func (h *AnalyticsHandler) processOrchestratorDiscoveryObserved(ctx context.Context, event kafka.AnalyticsEvent) error {
	var msg ipcpb.OrchestratorDiscoveryObserved
	if err := h.parseProtobufData(event, &msg); err != nil {
		return fmt.Errorf("parse orchestrator_discovery_observed: %w", err)
	}

	ident, err := requireOrchestratorIdentity(event)
	if err != nil {
		return err
	}

	orchAddr := msg.GetOrchAddr()
	tenantID, ok := parseTenantID(event.TenantID)
	if !ok {
		return fmt.Errorf("orchestrator_discovery_observed invalid tenant_id %q", event.TenantID)
	}

	vantage := msg.GetVantage()
	if vantage == nil {
		// Discovery without resolved IP / geo is still useful. Record with empty
		// resolved_ip so the row is unique by (gateway, orch_addr, resolved_ip="").
		vantage = &ipcpb.OrchestratorVantageGeo{}
	}

	batch, err := periscopeingestdb.PrepareOrchestratorDiscoverySample(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("prepare orchestrator_discovery_samples batch: %w", err)
	}
	defer func() { _ = batch.Close() }()

	if err := batch.Append(periscopeingestdb.OrchestratorDiscoverySampleRow{
		Timestamp: event.Timestamp, TenantID: tenantID, GatewayID: ident.gatewayID, GatewayRegion: ident.gatewayRegion,
		OrchAddr: orchAddr, OrchURL: msg.GetOrchUrl(), ResolvedIP: vantage.GetResolvedIp(), AdvertisedNodeURL: msg.GetAdvertisedNodeUrl(),
		DiscoveryLatencyMS: msg.GetDiscoveryLatencyMs(), Reachable: boolToUInt8(msg.GetReachable()),
		Compatible: boolToUInt8(msg.GetCompatible()), Score: msg.GetScore(), Dialed: boolToUInt8(vantage.GetDialed()),
		FailureReason: msg.GetFailureReason(), FailureKind: msg.GetFailureKind(),
		Latitude: vantage.GetLatitude(), Longitude: vantage.GetLongitude(), CountryCode: vantage.GetCountryCode(),
		GeoSource: geoSourceOrUnknown(vantage.GetGeoSource()),
	}); err != nil {
		return fmt.Errorf("append orchestrator_discovery_samples: %w", err)
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("send orchestrator_discovery_samples batch: %w", err)
	}

	// Every dialed observation refreshes the per-vantage current row:
	// success AND failure. The current table must reflect the latest dial
	// state so the map can show "stale / last attempt failed" when a path
	// degrades, instead of keeping an old success row visible until the
	// next success arrives. Sibling A-record rows (dialed=false) are
	// discovery context, not observations of the path itself.
	if vantage.GetDialed() {
		latency := msg.GetDiscoveryLatencyMs()
		if !msg.GetReachable() {
			// Failed dial: keep latency=0 in the current row so the UI can
			// render "stale" via dialed_recently false logic, but preserve
			// the timestamp so the row doesn't appear to have stopped
			// reporting entirely.
			latency = 0
		}
		if err := h.upsertOrchestratorVantage(ctx, event, ident, orchAddr, vantage, latency, msg.GetScore(), msg.GetReachable()); err != nil {
			h.logger.WithError(err).WithFields(logging.Fields{
				"orch_addr":   orchAddr,
				"resolved_ip": vantage.GetResolvedIp(),
				"reachable":   msg.GetReachable(),
			}).Warn("orchestrator_vantage_current upsert failed")
		}
	}

	return nil
}

// processOrchestratorStateUpdate writes per-instance orchestrator state. One
// observation = one instance behind the orch's load-balanced DNS, identified
// by the resolved IP the gateway dialed. Pricing, capabilities, hardware,
// and advertised sub-nodes can differ across instances even under the same
// eth address. This is usually consistent in practice, but not guaranteed; see
// docs/architecture/orchestrator-visibility.md.
//
// Writes go to two tables:
//   - orchestrator_state_current: identity-level row (orch_addr, last_seen)
//     so the federation map can list "all known orchs under this tenant"
//     without scanning every instance.
//   - orchestrator_instance_state_current: per-instance facts keyed by
//     (tenant, orch_addr, resolved_ip). The side panel reads from here for
//     the instance breakdown.
//
// The vantage row (per-(gateway, instance) latency/score/geo) is upserted
// separately when the observation includes vantage data.
func (h *AnalyticsHandler) processOrchestratorStateUpdate(ctx context.Context, event kafka.AnalyticsEvent) error {
	var msg ipcpb.OrchestratorStateUpdate
	if err := h.parseProtobufData(event, &msg); err != nil {
		return fmt.Errorf("parse orchestrator_state_update: %w", err)
	}

	ident, err := requireOrchestratorIdentity(event)
	if err != nil {
		return err
	}

	now := time.Now()
	tenantID, ok := parseTenantID(event.TenantID)
	if !ok {
		return fmt.Errorf("orchestrator_state_update invalid tenant_id %q", event.TenantID)
	}
	source := msg.GetSource()
	if source == "" {
		source = "gateway_pool"
	}

	// Identity row for listing known orchestrators without scanning instances.
	stateBatch, err := periscopeingestdb.PrepareOrchestratorStateCurrent(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("prepare orchestrator_state_current batch: %w", err)
	}
	defer func() { _ = stateBatch.Close() }()
	if err := stateBatch.Append(periscopeingestdb.OrchestratorStateCurrentRow{
		TenantID: tenantID, OrchAddr: msg.GetOrchAddr(), LastSeen: event.Timestamp, Metadata: "{}", UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("append orchestrator_state_current: %w", err)
	}
	if err := stateBatch.Send(); err != nil {
		return fmt.Errorf("send orchestrator_state_current batch: %w", err)
	}

	// Per-instance state requires a resolved IP to avoid collapsing distinct
	// instances behind one orchestrator address.
	resolvedIP := ""
	if vantage := msg.GetVantage(); vantage != nil {
		resolvedIP = vantage.GetResolvedIp()
	}
	if resolvedIP != "" {
		instanceBatch, err := periscopeingestdb.PrepareOrchestratorInstanceStateCurrent(ctx, h.clickhouse)
		if err != nil {
			return fmt.Errorf("prepare orchestrator_instance_state_current batch: %w", err)
		}
		defer func() { _ = instanceBatch.Close() }()
		capabilities, positions, pricePerUnits, pixelsPerUnits := capabilityPriceArrays(msg.GetCapabilityPriceEntries())
		if err := instanceBatch.Append(periscopeingestdb.OrchestratorInstanceStateCurrentRow{
			TenantID: tenantID, OrchAddr: msg.GetOrchAddr(), ResolvedIP: resolvedIP,
			CanonicalURL: msg.GetCanonicalUrl(), AdvertisedNodeURLs: msg.GetAdvertisedNodeUrls(), Capabilities: msg.GetCapabilities(),
			PricePerUnit: msg.GetPricePerUnit(), PixelsPerUnit: msg.GetPixelsPerUnit(),
			CapabilityPriceCapabilities: capabilities, CapabilityPricePositions: positions,
			CapabilityPricePricePerUnits: pricePerUnits, CapabilityPricePixelsPerUnits: pixelsPerUnits,
			Hardware: msg.GetHardware(), Source: source, LastSeen: event.Timestamp, Metadata: "{}", UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("append orchestrator_instance_state_current: %w", err)
		}
		if err := instanceBatch.Send(); err != nil {
			return fmt.Errorf("send orchestrator_instance_state_current batch: %w", err)
		}

		// State updates do not refresh orchestrator_vantage_current because
		// they do not carry latency or score data.
	} else {
		h.logger.WithFields(logging.Fields{
			"orch_addr":  msg.GetOrchAddr(),
			"gateway_id": ident.gatewayID,
		}).Warn("orchestrator_state_update without resolved_ip — skipping per-instance write")
	}

	return nil
}

func capabilityPriceArrays(entries []*ipcpb.OrchestratorCapabilityPriceEntry) ([]string, []uint32, []int64, []int64) {
	capabilities := make([]string, 0, len(entries))
	positions := make([]uint32, 0, len(entries))
	pricePerUnits := make([]int64, 0, len(entries))
	pixelsPerUnits := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		capabilities = append(capabilities, entry.GetCapability())
		positions = append(positions, entry.GetPosition())
		pricePerUnits = append(pricePerUnits, entry.GetPricePerUnit())
		pixelsPerUnits = append(pixelsPerUnits, entry.GetPixelsPerUnit())
	}
	return capabilities, positions, pricePerUnits, pixelsPerUnits
}

// processOrchestratorTranscodeOutcome writes per-segment / per-session
// transcode outcomes (success or failure) into orchestrator_transcode_outcomes.
// tenant_id is the stream tenant; cluster_owner_tenant_id is the gateway
// cluster owner.
func (h *AnalyticsHandler) processOrchestratorTranscodeOutcome(ctx context.Context, event kafka.AnalyticsEvent) error {
	var msg ipcpb.OrchestratorTranscodeOutcome
	if err := h.parseProtobufData(event, &msg); err != nil {
		return fmt.Errorf("parse orchestrator_transcode_outcome: %w", err)
	}

	ident, err := requireOrchestratorIdentity(event)
	if err != nil {
		return err
	}

	clusterOwner := stringFromData(event.Data, "cluster_owner_tenant_id")
	if clusterOwner == "" {
		return fmt.Errorf("orchestrator_transcode_outcome missing cluster_owner_tenant_id")
	}
	tenantID, ok := parseTenantID(event.TenantID)
	if !ok {
		return fmt.Errorf("orchestrator_transcode_outcome invalid tenant_id %q", event.TenantID)
	}
	clusterOwnerID, err := uuid.Parse(clusterOwner)
	if err != nil {
		return fmt.Errorf("orchestrator_transcode_outcome invalid cluster_owner_tenant_id %q: %w", clusterOwner, err)
	}

	batch, err := periscopeingestdb.PrepareOrchestratorTranscodeOutcome(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("prepare orchestrator_transcode_outcomes batch: %w", err)
	}
	defer func() { _ = batch.Close() }()

	if err := batch.Append(periscopeingestdb.OrchestratorTranscodeOutcomeRow{
		Timestamp: event.Timestamp, TenantID: tenantID, ClusterOwnerTenantID: clusterOwnerID,
		GatewayID: ident.gatewayID, GatewayRegion: ident.gatewayRegion, ClusterID: ident.clusterID,
		OrchAddr: msg.GetOrchAddr(), OrchURL: msg.GetOrchUrl(), ResolvedIP: msg.GetResolvedIp(),
		SessionID: msg.GetSessionId(), ManifestIDHash: msg.GetManifestIdHash(), SeqNo: msg.GetSeqNo(),
		Success: boolToUInt8(msg.GetSuccess()), LatencyScore: msg.GetLatencyScore(), UploadMS: msg.GetUploadMs(),
		TranscodeMS: msg.GetTranscodeMs(), OverallMS: msg.GetOverallMs(), Pixels: msg.GetPixels(), Profiles: msg.GetProfiles(),
		ErrorCode: msg.GetErrorCode(), ErrorKind: msg.GetErrorKind(),
	}); err != nil {
		return fmt.Errorf("append orchestrator_transcode_outcomes: %w", err)
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("send orchestrator_transcode_outcomes batch: %w", err)
	}

	return nil
}

// processOrchestratorAIOutcome writes per-AI-job outcomes into a separate
// table; pricing meters and consumers differ from transcode (GPU-hour vs
// delivered minutes are not interchangeable).
func (h *AnalyticsHandler) processOrchestratorAIOutcome(ctx context.Context, event kafka.AnalyticsEvent) error {
	var msg ipcpb.OrchestratorAIOutcome
	if err := h.parseProtobufData(event, &msg); err != nil {
		return fmt.Errorf("parse orchestrator_ai_outcome: %w", err)
	}

	ident, err := requireOrchestratorIdentity(event)
	if err != nil {
		return err
	}

	clusterOwner := stringFromData(event.Data, "cluster_owner_tenant_id")
	if clusterOwner == "" {
		return fmt.Errorf("orchestrator_ai_outcome missing cluster_owner_tenant_id")
	}
	tenantID, ok := parseTenantID(event.TenantID)
	if !ok {
		return fmt.Errorf("orchestrator_ai_outcome invalid tenant_id %q", event.TenantID)
	}
	clusterOwnerID, err := uuid.Parse(clusterOwner)
	if err != nil {
		return fmt.Errorf("orchestrator_ai_outcome invalid cluster_owner_tenant_id %q: %w", clusterOwner, err)
	}

	batch, err := periscopeingestdb.PrepareOrchestratorAIOutcome(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("prepare orchestrator_ai_outcomes batch: %w", err)
	}
	defer func() { _ = batch.Close() }()

	if err := batch.Append(periscopeingestdb.OrchestratorAIOutcomeRow{
		Timestamp: event.Timestamp, TenantID: tenantID, ClusterOwnerTenantID: clusterOwnerID,
		GatewayID: ident.gatewayID, GatewayRegion: ident.gatewayRegion, ClusterID: ident.clusterID,
		OrchAddr: msg.GetOrchAddr(), OrchURL: msg.GetOrchUrl(), ResolvedIP: msg.GetResolvedIp(),
		SessionID: msg.GetSessionId(), Pipeline: msg.GetPipeline(), Model: msg.GetModel(),
		LatencyScore: msg.GetLatencyScore(), PricePerUnit: msg.GetPricePerUnit(), LatencyMS: msg.GetLatencyMs(),
		Success: boolToUInt8(msg.GetSuccess()), ErrorCode: msg.GetErrorCode(), ErrorKind: msg.GetErrorKind(),
	}); err != nil {
		return fmt.Errorf("append orchestrator_ai_outcomes: %w", err)
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("send orchestrator_ai_outcomes batch: %w", err)
	}

	return nil
}

// upsertOrchestratorVantage refreshes a per-vantage current row. ReplacingMergeTree
// uses the provided updated_at column as the version, so concurrent inserts
// across gateways for the same (tenant, gateway, orch, resolved_ip) converge
// to the latest observation. `dialedRecently` carries reachability of the
// most recent dial — failures still update the row (so a degraded path
// doesn't keep showing the last good measurement), with dialedRecently=false
// signaling "last attempt failed".
func (h *AnalyticsHandler) upsertOrchestratorVantage(ctx context.Context, event kafka.AnalyticsEvent, ident orchestratorIdent, orchAddr string, vantage *ipcpb.OrchestratorVantageGeo, latencyMs uint32, score float32, dialedRecently bool) error {
	tenantID, ok := parseTenantID(event.TenantID)
	if !ok {
		return fmt.Errorf("orchestrator_vantage_current invalid tenant_id %q", event.TenantID)
	}
	batch, err := periscopeingestdb.PrepareOrchestratorVantageCurrent(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("prepare orchestrator_vantage_current batch: %w", err)
	}
	defer func() { _ = batch.Close() }()

	geoResolvedAt := time.Time{}
	if t := vantage.GetGeoResolvedAt(); t != nil {
		geoResolvedAt = t.AsTime()
	}

	now := time.Now()
	if err := batch.Append(periscopeingestdb.OrchestratorVantageCurrentRow{
		TenantID: tenantID, GatewayID: ident.gatewayID, GatewayRegion: ident.gatewayRegion,
		OrchAddr: orchAddr, ResolvedIP: vantage.GetResolvedIp(), Latitude: vantage.GetLatitude(), Longitude: vantage.GetLongitude(),
		City: vantage.GetCity(), CountryCode: vantage.GetCountryCode(), GeoSource: geoSourceOrUnknown(vantage.GetGeoSource()),
		GeoResolvedAt: geoResolvedAt, LatestLatencyMS: latencyMs, Score: score, DialedRecently: boolToUInt8(dialedRecently),
		LastSeen: event.Timestamp, UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("append orchestrator_vantage_current: %w", err)
	}

	return batch.Send()
}

// orchestratorIdent is the (gateway, cluster) tuple every orchestrator event
// must carry. Pulled from event.Data because the gateway identity travels
// alongside the inner payload, not inside it.
type orchestratorIdent struct {
	gatewayID     string
	gatewayRegion string
	clusterID     string
}

func requireOrchestratorIdentity(event kafka.AnalyticsEvent) (orchestratorIdent, error) {
	ident := orchestratorIdent{
		gatewayID:     stringFromData(event.Data, "gateway_id"),
		gatewayRegion: stringFromData(event.Data, "gateway_region"),
		clusterID:     stringFromData(event.Data, "cluster_id"),
	}
	if ident.gatewayID == "" || ident.clusterID == "" {
		return ident, fmt.Errorf("orchestrator event missing gateway_id or cluster_id (event_id=%s)", event.EventID)
	}
	return ident, nil
}

func stringFromData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}

func geoSourceOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
