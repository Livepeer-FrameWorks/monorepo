package control

// Ownership epochs order different publishers. A cell-local revision orders
// transitions of one owner only; equal epochs must retain the same identity.
// When exactly one view is legacy (no epoch), only the revision orders them:
// see legacyAndFencedOwnership.
func acceptPublisherAdvertisement(current, incoming Location) bool {
	if current.SourceLeaseEpoch == 0 && incoming.SourceLeaseEpoch == 0 {
		return true
	}
	if legacyAndFencedOwnership(current, incoming) {
		return incoming.SourceRevision > current.SourceRevision
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

// legacyAndFencedOwnership reports whether exactly one of two views of one
// cell's Location carries an ownership epoch. A legacy owner is an outage
// admission (or a pre-fencing edge): the cell's database admitted it under the
// stream lock after the fenced owner ended, and both transitions drew their
// revision from that one per-stream counter. No epoch can order the two, so the
// revision does: the legacy owner replaces a fenced owner only with a later
// revision, and a delayed fenced write cannot resurrect an owner the legacy one
// replaced.
func legacyAndFencedOwnership(a, b Location) bool {
	return (a.SourceLeaseEpoch == 0) != (b.SourceLeaseEpoch == 0)
}
