package handlers

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"

	"github.com/gin-gonic/gin"
)

type streamRegistryDebugLocation struct {
	ClusterID        string   `json:"cluster_id"`
	IsOrigin         bool     `json:"is_origin"`
	IsLiveNow        bool     `json:"is_live_now"`
	SourceNodes      []string `json:"source_nodes,omitempty"`
	EdgeCount        int      `json:"edge_count,omitempty"`
	AdTimestamp      int64    `json:"ad_timestamp,omitempty"`
	ReplicatingFrom  string   `json:"replicating_from,omitempty"`
	PullDTSCURL      string   `json:"pull_dtsc_url,omitempty"`
	DestNodeID       string   `json:"dest_node_id,omitempty"`
	DestNodeBaseURL  string   `json:"dest_node_base_url,omitempty"`
	PullSourceNodeID string   `json:"pull_source_node_id,omitempty"`
	OutboundCount    int      `json:"outbound_count,omitempty"`
	UpdatedAt        string   `json:"updated_at,omitempty"`

	// Source-presence admission state (per-Location, local cluster only).
	// Operators inspecting "why was my push rejected?" or "did takeover
	// fire?" need these fields. SourceActive flips true on accepted
	// PUSH_REWRITE, false on PUSH_INPUT_CLOSE or STREAM_END.
	SourceActive     bool   `json:"source_active,omitempty"`
	SourceInactiveAt string `json:"source_inactive_at,omitempty"`
	OwnerNodeID      string `json:"owner_node_id,omitempty"`
}

type streamRegistryDebugSource struct {
	StreamID        string                                 `json:"stream_id,omitempty"`
	TenantID        string                                 `json:"tenant_id,omitempty"`
	PlaybackID      string                                 `json:"playback_id,omitempty"`
	InternalName    string                                 `json:"internal_name"`
	IngestMode      string                                 `json:"ingest_mode,omitempty"`
	RuntimeName     string                                 `json:"runtime_name,omitempty"`
	OriginClusterID string                                 `json:"origin_cluster_id,omitempty"`
	HydratedAt      string                                 `json:"hydrated_at,omitempty"`
	Locations       map[string]streamRegistryDebugLocation `json:"locations"`
}

type streamRegistryDebugArtifact struct {
	Kind            string `json:"kind"`
	ArtifactHash    string `json:"artifact_hash"`
	InternalName    string `json:"internal_name,omitempty"`
	StreamID        string `json:"stream_id,omitempty"`
	StreamInternal  string `json:"stream_internal,omitempty"`
	TenantID        string `json:"tenant_id,omitempty"`
	Status          string `json:"status,omitempty"`
	RuntimeName     string `json:"runtime_name,omitempty"`
	OriginClusterID string `json:"origin_cluster_id,omitempty"`
	StorageCluster  string `json:"storage_cluster,omitempty"`
	HasThumbnails   bool   `json:"has_thumbnails,omitempty"`
	HydrationSrc    string `json:"hydration_src,omitempty"`
	HydratedAt      string `json:"hydrated_at,omitempty"`
}

type streamRegistryDebugReplication struct {
	InternalName     string `json:"internal_name"`
	ReplicatingFrom  string `json:"replicating_from"`
	PullDTSCURL      string `json:"pull_dtsc_url"`
	DestNodeID       string `json:"dest_node_id"`
	DestNodeBaseURL  string `json:"dest_node_base_url"`
	PullSourceNodeID string `json:"pull_source_node_id"`
	UpdatedAt        string `json:"updated_at"`
}

type streamRegistryDebugResponse struct {
	GeneratedAt       string                           `json:"generated_at"`
	LocalClusterID    string                           `json:"local_cluster_id,omitempty"`
	Sources           []streamRegistryDebugSource      `json:"sources"`
	Artifacts         []streamRegistryDebugArtifact    `json:"artifacts,omitempty"`
	LocalReplications []streamRegistryDebugReplication `json:"local_replications,omitempty"`
}

// HandleStreamRegistry exposes the unified stream registry state for
// debugging routing decisions. Query params:
//   - q: substring filter on internal_name / playback_id
//   - include: comma-separated subset of {sources,artifacts,replicable,replications}; default all
func HandleStreamRegistry(c *gin.Context) {
	if control.StreamRegistryInstance == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stream registry not configured"})
		return
	}
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	includeSet := map[string]struct{}{}
	for _, s := range strings.Split(c.DefaultQuery("include", "sources,artifacts,replicable,replications"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			includeSet[s] = struct{}{}
		}
	}
	include := func(k string) bool { _, ok := includeSet[k]; return ok }

	resp := streamRegistryDebugResponse{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		LocalClusterID: clusterID,
	}

	if include("sources") {
		for _, entry := range control.StreamRegistryInstance.Snapshot() {
			if q != "" && !strings.Contains(strings.ToLower(entry.InternalName), q) && !strings.Contains(strings.ToLower(entry.PlaybackID), q) {
				continue
			}
			locs := make(map[string]streamRegistryDebugLocation, len(entry.Locations))
			for cid, loc := range entry.Locations {
				locs[cid] = streamRegistryDebugLocation{
					ClusterID:        loc.ClusterID,
					IsOrigin:         loc.IsOrigin,
					IsLiveNow:        loc.IsLiveNow,
					SourceNodes:      loc.SourceNodes,
					EdgeCount:        len(loc.EdgeCandidates),
					AdTimestamp:      loc.AdTimestamp,
					ReplicatingFrom:  loc.ReplicatingFrom,
					PullDTSCURL:      loc.PullDTSCURL,
					DestNodeID:       loc.DestNodeID,
					DestNodeBaseURL:  loc.DestNodeBaseURL,
					PullSourceNodeID: loc.PullSourceNodeID,
					OutboundCount:    len(loc.OutboundPullers),
					UpdatedAt:        formatTime(loc.UpdatedAt),
					SourceActive:     loc.SourceActive,
					SourceInactiveAt: formatTime(loc.SourceInactiveAt),
					OwnerNodeID:      loc.OwnerNodeID,
				}
			}
			resp.Sources = append(resp.Sources, streamRegistryDebugSource{
				StreamID:        entry.StreamID,
				TenantID:        entry.TenantID,
				PlaybackID:      entry.PlaybackID,
				InternalName:    entry.InternalName,
				IngestMode:      entry.IngestMode.String(),
				RuntimeName:     entry.RuntimeName,
				OriginClusterID: entry.OriginClusterID,
				HydratedAt:      formatTime(entry.HydratedAt),
				Locations:       locs,
			})
		}
		sort.Slice(resp.Sources, func(i, j int) bool { return resp.Sources[i].InternalName < resp.Sources[j].InternalName })
	}

	if include("artifacts") {
		for _, art := range control.StreamRegistryInstance.SnapshotArtifacts() {
			if q != "" && !strings.Contains(strings.ToLower(art.ArtifactHash), q) && !strings.Contains(strings.ToLower(art.InternalName), q) {
				continue
			}
			resp.Artifacts = append(resp.Artifacts, debugArtifact(art))
		}
		sort.Slice(resp.Artifacts, func(i, j int) bool { return resp.Artifacts[i].ArtifactHash < resp.Artifacts[j].ArtifactHash })
	}

	if include("replications") {
		for name, loc := range control.StreamRegistryInstance.AllLocalReplications() {
			if q != "" && !strings.Contains(strings.ToLower(name), q) {
				continue
			}
			resp.LocalReplications = append(resp.LocalReplications, streamRegistryDebugReplication{
				InternalName:     name,
				ReplicatingFrom:  loc.ReplicatingFrom,
				PullDTSCURL:      loc.PullDTSCURL,
				DestNodeID:       loc.DestNodeID,
				DestNodeBaseURL:  loc.DestNodeBaseURL,
				PullSourceNodeID: loc.PullSourceNodeID,
				UpdatedAt:        formatTime(loc.UpdatedAt),
			})
		}
		sort.Slice(resp.LocalReplications, func(i, j int) bool {
			return resp.LocalReplications[i].InternalName < resp.LocalReplications[j].InternalName
		})
	}

	c.JSON(http.StatusOK, resp)
}

func debugArtifact(a control.ArtifactEntry) streamRegistryDebugArtifact {
	return streamRegistryDebugArtifact{
		Kind:            a.Kind.String(),
		ArtifactHash:    a.ArtifactHash,
		InternalName:    a.InternalName,
		StreamID:        a.StreamID,
		StreamInternal:  a.StreamInternal,
		TenantID:        a.TenantID,
		Status:          a.Status,
		RuntimeName:     a.RuntimeName,
		OriginClusterID: a.OriginClusterID,
		StorageCluster:  a.StorageCluster,
		HasThumbnails:   a.HasThumbnails,
		HydrationSrc:    a.HydrationSrc,
		HydratedAt:      formatTime(a.HydratedAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
