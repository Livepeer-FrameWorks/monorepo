package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertAPIRequest = `INSERT INTO api_requests (
	timestamp, tenant_id, source_node, source_event_id, ingested_at_ms, auth_type, operation_name, operation_type,
	request_count, error_count, total_duration_ms, total_complexity,
	llm_input_tokens, llm_output_tokens, llm_model, llm_provider,
	user_hashes, token_hashes, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type APIRequestRow struct {
	Timestamp                                               time.Time
	TenantID                                                uuid.UUID
	SourceNode                                              *string
	SourceEventID                                           string
	IngestedAtMS                                            int64
	AuthType                                                string
	OperationName                                           *string
	OperationType                                           string
	RequestCount, ErrorCount                                uint32
	TotalDurationMS                                         uint64
	TotalComplexity                                         uint32
	LLMInputTokens, LLMOutputTokens                         uint64
	LLMModel, LLMProvider                                   string
	UserHashes, TokenHashes                                 []uint64
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareAPIRequest(ctx context.Context, db BatchPreparer) (*Writer[APIRequestRow], error) {
	return prepare(ctx, db, insertAPIRequest, func(row APIRequestRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.SourceNode, row.SourceEventID, row.IngestedAtMS, row.AuthType, row.OperationName, row.OperationType, row.RequestCount, row.ErrorCount, row.TotalDurationMS, row.TotalComplexity, row.LLMInputTokens, row.LLMOutputTokens, row.LLMModel, row.LLMProvider, row.UserHashes, row.TokenHashes, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertArtifactNodeCopyEvent = `INSERT INTO artifact_node_copy_events (
	event_id, timestamp, tenant_id, artifact_hash, node_id, role,
	transition, is_complete, size_bytes, version, source_region, schema_version
)`

type ArtifactNodeCopyEventRow struct {
	EventID                                string
	Timestamp                              time.Time
	TenantID                               uuid.UUID
	ArtifactHash, NodeID, Role, Transition string
	IsComplete                             bool
	SizeBytes                              *uint64
	Version                                uint64
	SourceRegion                           string
	SchemaVersion                          uint8
}

func PrepareArtifactNodeCopyEvent(ctx context.Context, db BatchPreparer) (*Writer[ArtifactNodeCopyEventRow], error) {
	return prepare(ctx, db, insertArtifactNodeCopyEvent, func(row ArtifactNodeCopyEventRow) []interface{} {
		return []interface{}{row.EventID, row.Timestamp, row.TenantID, row.ArtifactHash, row.NodeID, row.Role, row.Transition, row.IsComplete, row.SizeBytes, row.Version, row.SourceRegion, row.SchemaVersion}
	})
}

const insertArtifactNodeCopyCurrent = `INSERT INTO artifact_node_copy_current (
	tenant_id, artifact_hash, node_id, role, present, is_complete, size_bytes, version, updated_at
)`

type ArtifactNodeCopyCurrentRow struct {
	TenantID                   uuid.UUID
	ArtifactHash, NodeID, Role string
	Present, IsComplete        bool
	SizeBytes                  *uint64
	Version                    uint64
	UpdatedAt                  time.Time
}

func PrepareArtifactNodeCopyCurrent(ctx context.Context, db BatchPreparer) (*Writer[ArtifactNodeCopyCurrentRow], error) {
	return prepare(ctx, db, insertArtifactNodeCopyCurrent, func(row ArtifactNodeCopyCurrentRow) []interface{} {
		return []interface{}{row.TenantID, row.ArtifactHash, row.NodeID, row.Role, row.Present, row.IsComplete, row.SizeBytes, row.Version, row.UpdatedAt}
	})
}

const insertAPIEvent = `INSERT INTO api_events (
	event_id, tenant_id, event_type, source, user_id, resource_type, resource_id, details, timestamp,
	cluster_id, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type APIEventRow struct {
	EventID, TenantID                                                  uuid.UUID
	EventType, Source                                                  string
	UserID                                                             *uuid.UUID
	ResourceType                                                       string
	ResourceID                                                         *string
	Details                                                            string
	Timestamp                                                          time.Time
	ClusterID, SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                                      uint8
}

func PrepareAPIEvent(ctx context.Context, db BatchPreparer) (*Writer[APIEventRow], error) {
	return prepare(ctx, db, insertAPIEvent, func(row APIEventRow) []interface{} {
		return []interface{}{row.EventID, row.TenantID, row.EventType, row.Source, row.UserID, row.ResourceType, row.ResourceID, row.Details, row.Timestamp, row.ClusterID, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertTenantAcquisitionEvent = `INSERT INTO tenant_acquisition_events (
	timestamp, tenant_id, user_id, signup_channel, signup_method,
	utm_source, utm_medium, utm_campaign, utm_content, utm_term,
	http_referer, landing_page, referral_code, is_agent, event_data,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type TenantAcquisitionEventRow struct {
	Timestamp                                               time.Time
	TenantID                                                uuid.UUID
	UserID                                                  *uuid.UUID
	SignupChannel, SignupMethod                             string
	UTMSource, UTMMedium, UTMCampaign, UTMContent, UTMTerm  *string
	HTTPReferer, LandingPage, ReferralCode                  *string
	IsAgent                                                 uint8
	EventData                                               string
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareTenantAcquisitionEvent(ctx context.Context, db BatchPreparer) (*Writer[TenantAcquisitionEventRow], error) {
	return prepare(ctx, db, insertTenantAcquisitionEvent, func(row TenantAcquisitionEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.UserID, row.SignupChannel, row.SignupMethod, row.UTMSource, row.UTMMedium, row.UTMCampaign, row.UTMContent, row.UTMTerm, row.HTTPReferer, row.LandingPage, row.ReferralCode, row.IsAgent, row.EventData, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}
