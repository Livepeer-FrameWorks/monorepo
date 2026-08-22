package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertOrchestratorDiscoverySample = `INSERT INTO orchestrator_discovery_samples (
	timestamp, tenant_id, gateway_id, gateway_region,
	orch_addr, orch_url, resolved_ip, advertised_node_url,
	discovery_latency_ms, reachable, compatible, score, dialed,
	failure_reason, failure_kind,
	latitude, longitude, country_code, geo_source
)`

type OrchestratorDiscoverySampleRow struct {
	Timestamp          time.Time
	TenantID           uuid.UUID
	GatewayID          string
	GatewayRegion      string
	OrchAddr           string
	OrchURL            string
	ResolvedIP         string
	AdvertisedNodeURL  string
	DiscoveryLatencyMS uint32
	Reachable          uint8
	Compatible         uint8
	Score              float32
	Dialed             uint8
	FailureReason      string
	FailureKind        string
	Latitude           float64
	Longitude          float64
	CountryCode        string
	GeoSource          string
}

func PrepareOrchestratorDiscoverySample(ctx context.Context, db BatchPreparer) (*Writer[OrchestratorDiscoverySampleRow], error) {
	return prepare(ctx, db, insertOrchestratorDiscoverySample, func(row OrchestratorDiscoverySampleRow) []interface{} {
		return []interface{}{
			row.Timestamp, row.TenantID, row.GatewayID, row.GatewayRegion,
			row.OrchAddr, row.OrchURL, row.ResolvedIP, row.AdvertisedNodeURL,
			row.DiscoveryLatencyMS, row.Reachable, row.Compatible, row.Score, row.Dialed,
			row.FailureReason, row.FailureKind,
			row.Latitude, row.Longitude, row.CountryCode, row.GeoSource,
		}
	})
}

const insertOrchestratorStateCurrent = `INSERT INTO orchestrator_state_current (
	tenant_id, orch_addr, last_seen, metadata, updated_at
)`

type OrchestratorStateCurrentRow struct {
	TenantID  uuid.UUID
	OrchAddr  string
	LastSeen  time.Time
	Metadata  string
	UpdatedAt time.Time
}

func PrepareOrchestratorStateCurrent(ctx context.Context, db BatchPreparer) (*Writer[OrchestratorStateCurrentRow], error) {
	return prepare(ctx, db, insertOrchestratorStateCurrent, func(row OrchestratorStateCurrentRow) []interface{} {
		return []interface{}{row.TenantID, row.OrchAddr, row.LastSeen, row.Metadata, row.UpdatedAt}
	})
}

const insertOrchestratorInstanceStateCurrent = `INSERT INTO orchestrator_instance_state_current (
	tenant_id, orch_addr, resolved_ip,
	canonical_url, advertised_node_urls, capabilities,
	price_per_unit, pixels_per_unit,
	capability_price_capabilities, capability_price_positions,
	capability_price_price_per_units, capability_price_pixels_per_units,
	hardware, source,
	last_seen, metadata, updated_at
)`

type OrchestratorInstanceStateCurrentRow struct {
	TenantID                      uuid.UUID
	OrchAddr                      string
	ResolvedIP                    string
	CanonicalURL                  string
	AdvertisedNodeURLs            []string
	Capabilities                  []string
	PricePerUnit                  int64
	PixelsPerUnit                 int64
	CapabilityPriceCapabilities   []string
	CapabilityPricePositions      []uint32
	CapabilityPricePricePerUnits  []int64
	CapabilityPricePixelsPerUnits []int64
	Hardware                      string
	Source                        string
	LastSeen                      time.Time
	Metadata                      string
	UpdatedAt                     time.Time
}

func PrepareOrchestratorInstanceStateCurrent(ctx context.Context, db BatchPreparer) (*Writer[OrchestratorInstanceStateCurrentRow], error) {
	return prepare(ctx, db, insertOrchestratorInstanceStateCurrent, func(row OrchestratorInstanceStateCurrentRow) []interface{} {
		return []interface{}{
			row.TenantID, row.OrchAddr, row.ResolvedIP,
			row.CanonicalURL, row.AdvertisedNodeURLs, row.Capabilities,
			row.PricePerUnit, row.PixelsPerUnit,
			row.CapabilityPriceCapabilities, row.CapabilityPricePositions,
			row.CapabilityPricePricePerUnits, row.CapabilityPricePixelsPerUnits,
			row.Hardware, row.Source,
			row.LastSeen, row.Metadata, row.UpdatedAt,
		}
	})
}

const insertOrchestratorTranscodeOutcome = `INSERT INTO orchestrator_transcode_outcomes (
	timestamp, tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, cluster_id,
	orch_addr, orch_url, resolved_ip,
	session_id, manifest_id_hash, seq_no,
	success, latency_score, upload_ms, transcode_ms, overall_ms,
	pixels, profiles, error_code, error_kind
)`

type OrchestratorTranscodeOutcomeRow struct {
	Timestamp            time.Time
	TenantID             uuid.UUID
	ClusterOwnerTenantID uuid.UUID
	GatewayID            string
	GatewayRegion        string
	ClusterID            string
	OrchAddr             string
	OrchURL              string
	ResolvedIP           string
	SessionID            string
	ManifestIDHash       string
	SeqNo                uint64
	Success              uint8
	LatencyScore         float32
	UploadMS             uint32
	TranscodeMS          uint32
	OverallMS            uint32
	Pixels               uint64
	Profiles             []string
	ErrorCode            string
	ErrorKind            string
}

func PrepareOrchestratorTranscodeOutcome(ctx context.Context, db BatchPreparer) (*Writer[OrchestratorTranscodeOutcomeRow], error) {
	return prepare(ctx, db, insertOrchestratorTranscodeOutcome, func(row OrchestratorTranscodeOutcomeRow) []interface{} {
		return []interface{}{
			row.Timestamp, row.TenantID, row.ClusterOwnerTenantID, row.GatewayID, row.GatewayRegion, row.ClusterID,
			row.OrchAddr, row.OrchURL, row.ResolvedIP,
			row.SessionID, row.ManifestIDHash, row.SeqNo,
			row.Success, row.LatencyScore, row.UploadMS, row.TranscodeMS, row.OverallMS,
			row.Pixels, row.Profiles, row.ErrorCode, row.ErrorKind,
		}
	})
}

const insertOrchestratorAIOutcome = `INSERT INTO orchestrator_ai_outcomes (
	timestamp, tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, cluster_id,
	orch_addr, orch_url, resolved_ip,
	session_id, pipeline, model,
	latency_score, price_per_unit, latency_ms,
	success, error_code, error_kind
)`

type OrchestratorAIOutcomeRow struct {
	Timestamp            time.Time
	TenantID             uuid.UUID
	ClusterOwnerTenantID uuid.UUID
	GatewayID            string
	GatewayRegion        string
	ClusterID            string
	OrchAddr             string
	OrchURL              string
	ResolvedIP           string
	SessionID            string
	Pipeline             string
	Model                string
	LatencyScore         float32
	PricePerUnit         int64
	LatencyMS            uint32
	Success              uint8
	ErrorCode            string
	ErrorKind            string
}

func PrepareOrchestratorAIOutcome(ctx context.Context, db BatchPreparer) (*Writer[OrchestratorAIOutcomeRow], error) {
	return prepare(ctx, db, insertOrchestratorAIOutcome, func(row OrchestratorAIOutcomeRow) []interface{} {
		return []interface{}{
			row.Timestamp, row.TenantID, row.ClusterOwnerTenantID, row.GatewayID, row.GatewayRegion, row.ClusterID,
			row.OrchAddr, row.OrchURL, row.ResolvedIP,
			row.SessionID, row.Pipeline, row.Model,
			row.LatencyScore, row.PricePerUnit, row.LatencyMS,
			row.Success, row.ErrorCode, row.ErrorKind,
		}
	})
}

const insertOrchestratorVantageCurrent = `INSERT INTO orchestrator_vantage_current (
	tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
	latitude, longitude, city, country_code, geo_source, geo_resolved_at,
	latest_latency_ms, score, dialed_recently, last_seen, updated_at
)`

type OrchestratorVantageCurrentRow struct {
	TenantID        uuid.UUID
	GatewayID       string
	GatewayRegion   string
	OrchAddr        string
	ResolvedIP      string
	Latitude        float64
	Longitude       float64
	City            string
	CountryCode     string
	GeoSource       string
	GeoResolvedAt   time.Time
	LatestLatencyMS uint32
	Score           float32
	DialedRecently  uint8
	LastSeen        time.Time
	UpdatedAt       time.Time
}

func PrepareOrchestratorVantageCurrent(ctx context.Context, db BatchPreparer) (*Writer[OrchestratorVantageCurrentRow], error) {
	return prepare(ctx, db, insertOrchestratorVantageCurrent, func(row OrchestratorVantageCurrentRow) []interface{} {
		return []interface{}{
			row.TenantID, row.GatewayID, row.GatewayRegion, row.OrchAddr, row.ResolvedIP,
			row.Latitude, row.Longitude, row.City, row.CountryCode, row.GeoSource, row.GeoResolvedAt,
			row.LatestLatencyMS, row.Score, row.DialedRecently, row.LastSeen, row.UpdatedAt,
		}
	})
}
