package triggers

import (
	"context"

	"frameworks/api_balancing/internal/control"
)

// localDVRSourcePull binds an in-cell edge pull to the same per-attempt
// credential and fresh recording-owner check used for cross-cell pulls.
func (p *Processor) localDVRSourcePull(ctx context.Context, runtimeName string, recording *control.DVRArtifactDispatch, destinationNode string) string {
	registry := control.StreamRegistryInstance
	if recording == nil || registry == nil || runtimeName != "dvr+"+recording.InternalName ||
		!control.DVRRecordingSource(ctx, control.GetDB(), recording.TenantID, recording.InternalName, recording.RecordingNode) {
		return ""
	}
	destinationCluster := p.resolveNodeClusterIDWithContext(ctx, destinationNode)
	sourceCluster := p.resolveNodeClusterIDWithContext(ctx, recording.RecordingNode)
	base := control.BuildDTSCURI(recording.RecordingNode, runtimeName, p.logger)
	if destinationCluster == "" || sourceCluster == "" || base == "" {
		return ""
	}
	pull, err := registry.RecordOutboundPull(ctx, runtimeName, control.OutboundPull{
		TenantID: recording.TenantID, SourceMediaClusterID: sourceCluster, SourceNodeID: recording.RecordingNode,
		DestClusterID: destinationCluster, DestNodeID: destinationNode, DTSCURL: base,
	})
	if err != nil {
		return ""
	}
	result, err := control.SourcePullURL(base, runtimeName, pull)
	if err != nil || ctx.Err() != nil {
		return ""
	}
	return result
}
