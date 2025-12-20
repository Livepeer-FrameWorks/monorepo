package graph

import (
	"context"

	"frameworks/api_gateway/internal/loaders"
	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"
)

// getLifecycleData fetches artifact lifecycle data from the ArtifactLifecycleLoader.
// Used by clip resolvers to get processing status, file paths, etc.
func (r *clipResolver) getLifecycleData(ctx context.Context, requestID string) *pb.ArtifactState {
	if requestID == "" {
		return nil
	}

	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil
	}

	lds := loaders.FromContext(ctx)
	if lds == nil || lds.ArtifactLifecycle == nil {
		return nil
	}

	state, err := lds.ArtifactLifecycle.Load(ctx, tenantID, requestID)
	if err != nil {
		return nil
	}

	return state
}

// getLifecycleData fetches artifact lifecycle data from the ArtifactLifecycleLoader.
// Used by DVR resolvers to get processing status, file paths, etc.
func (r *dVRRequestResolver) getLifecycleData(ctx context.Context, requestID string) *pb.ArtifactState {
	if requestID == "" {
		return nil
	}

	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil
	}

	lds := loaders.FromContext(ctx)
	if lds == nil || lds.ArtifactLifecycle == nil {
		return nil
	}

	state, err := lds.ArtifactLifecycle.Load(ctx, tenantID, requestID)
	if err != nil {
		return nil
	}

	return state
}

// formatMetricName formats a metric key for display in invoice line items
func formatMetricName(metric string) string {
	names := map[string]string{
		"viewer_hours":     "Delivered Minutes",
		"storage_gb_hours": "Storage (GB-hours)",
		"bandwidth_gb":     "Bandwidth (GB)",
		"ingest_hours":     "Ingest Hours",
		"transcode_hours":  "Transcode Hours",
	}
	if name, ok := names[metric]; ok {
		return name
	}
	return metric
}
