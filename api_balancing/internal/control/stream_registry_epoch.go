package control

// Ownership epochs order different publishers. A cell-local revision orders
// transitions of one owner only; equal epochs must retain the same identity.
func acceptPublisherAdvertisement(current, incoming Location) bool {
	if current.SourceLeaseEpoch == 0 && incoming.SourceLeaseEpoch == 0 {
		return true
	}
	if incoming.SourceLeaseEpoch < current.SourceLeaseEpoch {
		return false
	}
	if incoming.SourceLeaseEpoch > current.SourceLeaseEpoch {
		return true
	}
	if incoming.SourceGeneration != current.SourceGeneration || incoming.OwnerNodeID != current.OwnerNodeID ||
		incoming.SourceRuntimeName != current.SourceRuntimeName || incoming.SourceClusterID != current.SourceClusterID {
		return false
	}
	if incoming.SourceRevision < current.SourceRevision {
		return false
	}
	return incoming.SourceRevision != current.SourceRevision || incoming.SourceActive == current.SourceActive
}
