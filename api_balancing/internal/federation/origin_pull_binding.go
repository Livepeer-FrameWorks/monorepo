package federation

import (
	"strings"
	"unicode"

	"frameworks/api_balancing/internal/control"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"github.com/google/uuid"
)

func validPullGeneration(generation string, revision int64) bool {
	if generation == "" {
		return revision == 0
	}
	return revision > 0 && len(generation) <= 255 && strings.TrimSpace(generation) == generation && strings.IndexFunc(generation, unicode.IsControl) < 0
}

func canonicalPullAttempt(attempt string) bool {
	id, err := uuid.Parse(attempt)
	return err == nil && id != uuid.Nil && id.String() == attempt
}

// A receipt-bound publisher pull must be accepted by its exact source cell,
// generation and node before its URL is exposed. A generationless relay hint
// cannot establish a loop after that publisher has moved to another cell.
func requiresPublisherOriginAcceptance(req ArrangeOriginPullRequest) bool {
	if req.BindPull == nil || req.DestinationFence <= 0 || req.SourceRevision <= 0 || req.SourceLeaseEpoch <= 0 || req.Remote == nil ||
		!validPullIdentity(req.RemoteCluster) || !validPullIdentity(req.Remote.GetClusterId()) || !validPullIdentity(req.Remote.GetNodeId()) {
		return false
	}
	generation, err := uuid.Parse(req.SourceGeneration)
	return err == nil && generation != uuid.Nil && generation.String() == req.SourceGeneration
}

func validPullIdentity(identity string) bool {
	return identity != "" && len(identity) <= 255 && strings.TrimSpace(identity) == identity && strings.IndexFunc(identity, unicode.IsControl) < 0
}

func bindOriginPullRequest(req *ArrangeOriginPullRequest) error {
	if req.Remote.GetClusterId() != "" && (!validPullIdentity(req.Remote.GetClusterId()) || !validPullIdentity(req.RemoteCluster) || !validPullIdentity(req.Remote.GetNodeId())) {
		return ErrOriginPullSourceBinding
	}
	if !validPullGeneration(req.SourceGeneration, req.SourceRevision) {
		return ErrOriginPullSourceBinding
	}
	if req.Remote.GetSourceGeneration() != "" || req.Remote.GetSourceRevision() != 0 {
		if req.SourceGeneration != "" && (req.SourceGeneration != req.Remote.GetSourceGeneration() || req.SourceRevision != req.Remote.GetSourceRevision()) {
			return ErrOriginPullSourceBinding
		}
		req.SourceGeneration, req.SourceRevision = req.Remote.GetSourceGeneration(), req.Remote.GetSourceRevision()
	}
	if req.Remote.GetSourceLeaseEpoch() < 0 || req.SourceLeaseEpoch < 0 {
		return ErrOriginPullSourceBinding
	}
	if req.Remote.GetSourceLeaseEpoch() != 0 {
		if req.SourceLeaseEpoch != 0 && req.SourceLeaseEpoch != req.Remote.GetSourceLeaseEpoch() {
			return ErrOriginPullSourceBinding
		}
		req.SourceLeaseEpoch = req.Remote.GetSourceLeaseEpoch()
	}
	if req.Remote.GetSourceRuntimeName() != "" {
		if req.SourceRuntimeName != "" && req.SourceRuntimeName != req.Remote.GetSourceRuntimeName() {
			return ErrOriginPullSourceBinding
		}
		req.SourceRuntimeName = req.Remote.GetSourceRuntimeName()
	}
	if !validPullGeneration(req.SourceGeneration, req.SourceRevision) || (req.SourceGeneration != "" && req.Remote.GetNodeId() == "") {
		return ErrOriginPullSourceBinding
	}
	if req.AttemptID == "" {
		req.AttemptID = uuid.NewString()
	}
	if !canonicalPullAttempt(req.AttemptID) {
		return ErrOriginPullSourceBinding
	}
	if req.SourceRuntimeName != "" {
		binding, ok := streamident.ParseRuntimeBinding(req.SourceRuntimeName)
		if !ok || binding.Role != streamident.RuntimePublisher || binding.InternalName != req.InternalName || binding.ExecutionID != req.SourceGeneration || req.SourceLeaseEpoch <= 0 {
			return ErrOriginPullSourceBinding
		}
		name, err := streamident.BoundRuntimeName(req.InternalName, streamident.RuntimeRelay, req.AttemptID)
		if err != nil || (req.DestRuntimeName != "" && req.DestRuntimeName != name) {
			return ErrOriginPullSourceBinding
		}
	} else if req.DestRuntimeName != "" {
		return ErrOriginPullSourceBinding
	}
	return nil
}

func confirmedPublisherBinding(entry control.StreamEntry, nodeID, tenantID, clusterID string) (string, int64) {
	loc, found := entry.LocalLocation(clusterID)
	if !found || entry.TenantID != tenantID || entry.IngestMode != control.IngestPush || !loc.SourceActive || loc.OwnerNodeID != nodeID ||
		loc.SourceGeneration == "" || !validPullGeneration(loc.SourceGeneration, loc.SourceRevision) {
		return "", 0
	}
	return loc.SourceGeneration, loc.SourceRevision
}

func confirmedPublisherEpoch(entry control.StreamEntry, nodeID, tenantID, clusterID string) int64 {
	if generation, _ := confirmedPublisherBinding(entry, nodeID, tenantID, clusterID); generation == "" {
		return 0
	}
	loc, _ := entry.LocalLocation(clusterID)
	return loc.SourceLeaseEpoch
}

func validOriginPullNotification(req *foghornfederationpb.OriginPullNotification) bool {
	if req.GetSourceLeaseEpoch() < 0 || !control.ValidPublisherPullRuntimes(req.GetStreamName(), req.GetSourceGeneration(), req.GetAttemptId(), req.GetSourceRuntimeName(), req.GetDestRuntimeName(), req.GetSourceLeaseEpoch()) {
		return false
	}
	if req == nil || !validPullGeneration(req.GetSourceGeneration(), req.GetSourceRevision()) {
		return false
	}
	if req.GetAttemptId() != "" && !canonicalPullAttempt(req.GetAttemptId()) {
		return false
	}
	if req.GetSourceGeneration() != "" && (req.GetSourceNodeId() == "" || req.GetDestNodeId() == "" || req.GetAttemptId() == "") {
		return false
	}
	if req.GetSourceCellId() != "" || req.GetSourceClusterId() != "" {
		if !validPullIdentity(req.GetSourceCellId()) || !validPullIdentity(req.GetSourceClusterId()) || !validPullIdentity(req.GetSourceNodeId()) ||
			!validPullIdentity(req.GetDestClusterId()) || !validPullIdentity(req.GetDestNodeId()) || !canonicalPullAttempt(req.GetAttemptId()) {
			return false
		}
	}
	return true
}

func originPullAckMatches(req ArrangeOriginPullRequest, destClusterID, destNodeID string, ack *foghornfederationpb.OriginPullAck) bool {
	if ack.GetSourceLeaseEpoch() != req.SourceLeaseEpoch || ack.GetSourceRuntimeName() != req.SourceRuntimeName || ack.GetDestRuntimeName() != req.DestRuntimeName {
		return false
	}
	if req.Remote.GetClusterId() != "" && (ack.GetSourceCellId() != req.RemoteCluster || ack.GetSourceClusterId() != req.Remote.GetClusterId()) {
		return false
	}
	if ack.GetSourceCellId() != "" || ack.GetSourceClusterId() != "" {
		if ack.GetSourceCellId() != req.RemoteCluster || !validPullIdentity(ack.GetSourceClusterId()) || ack.GetSourceNodeId() != req.Remote.GetNodeId() ||
			ack.GetTenantId() != req.TenantID || ack.GetDestClusterId() != destClusterID || ack.GetDestNodeId() != destNodeID || ack.GetAttemptId() != req.AttemptID {
			return false
		}
	}
	if req.SourceGeneration == "" {
		return true
	}
	return ack.GetTenantId() == req.TenantID && ack.GetSourceGeneration() == req.SourceGeneration && ack.GetSourceRevision() == req.SourceRevision &&
		ack.GetAttemptId() == req.AttemptID && ack.GetSourceNodeId() == req.Remote.GetNodeId() && ack.GetDestClusterId() == destClusterID && ack.GetDestNodeId() == destNodeID
}

func confirmedPublisherRuntimeName(entry control.StreamEntry, node, tenant, cell string) string {
	if generation, _ := confirmedPublisherBinding(entry, node, tenant, cell); generation != "" {
		loc, _ := entry.LocalLocation(cell)
		return loc.SourceRuntimeName
	}
	return ""
}
