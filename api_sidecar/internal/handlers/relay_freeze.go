package handlers

// relayFreezeHandoff bridges relay-generated .dtsh sidecars into the
// freeze reconciler. The relay performs a direct presigned PUT to S3 when
// Foghorn has minted one on the most recent RelayResolve; this handoff is
// the backstop that catches sidecars the direct PUT missed (no cached
// resolve, PUT failure) — the periodic freeze pass walks for newly-written
// sidecars next to their media files and uploads them on its next pass.
type relayFreezeHandoff struct{}

// NewRelayFreezeHandoff returns the FreezeHandoff implementation used by main.go
// when constructing the relay. Returns an interface so the relay package can
// accept it without importing handlers.
func NewRelayFreezeHandoff() interface {
	OnLocalDtshGenerated(assetKind, assetHash, localPath string)
} {
	return &relayFreezeHandoff{}
}

func (h *relayFreezeHandoff) OnLocalDtshGenerated(kind, hash, localPath string) {
	if logger != nil {
		logger.WithField("asset_kind", kind).
			WithField("asset_hash", hash).
			WithField("local_path", localPath).
			Debug("Relay accepted .dtsh PUT; nudging storage check")
	}
	// Nudge the storage manager to re-scan; freeze candidate picks the
	// new sidecar up promptly instead of waiting for the next periodic
	// pass. Safe to call regardless of whether a freeze is needed —
	// TriggerStorageCheck is rate-limited internally.
	TriggerStorageCheck()
}
