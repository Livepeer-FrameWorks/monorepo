package control

// Offline reason strings returned in /source / STREAM_SOURCE bodies when
// a stream is not currently servable. Mist's input_balancer recognizes
// the `offline:` prefix and marks STRMSTAT_OFFLINE, producing a clean
// disconnect for the output. The suffix is operator-facing diagnostic
// info — players don't parse it, but it shows up in Mist's status
// reporting and lets operators distinguish "stream not configured" from
// "stream not recorded yet" without grepping Foghorn logs.
//
// Add new reasons here rather than ad-hoc strings at call sites so the
// taxonomy stays bounded and grep-discoverable.
const (
	// OfflineNotConfigured: stream/source not known to Commodore (not in
	// DB, not enabled, or otherwise unrecognized). Persistent — won't
	// recover without a config change.
	OfflineNotConfigured = "offline:not_configured"

	// OfflineDisabled: stream/source is configured but admin-disabled
	// (e.g., tenant disabled the pull source). Transient if admin
	// re-enables.
	OfflineDisabled = "offline:disabled"

	// OfflineBlockedURI: pull source upstream URI failed security
	// classification (blocklist). Persistent until URI changes.
	OfflineBlockedURI = "offline:blocked_uri"

	// OfflineInvalidUpstream: pull source upstream URI failed format
	// validation (couldn't score, malformed). Persistent.
	OfflineInvalidUpstream = "offline:invalid_upstream"

	// OfflineNotPlaced: stream eligible elsewhere but not on this
	// cluster, AND no peer cluster is currently serving it for federation.
	// Transient if a peer comes online or placement changes.
	OfflineNotPlaced = "offline:not_placed"

	// OfflineNotRecorded: DVR token resolved but no active recording
	// (recording finished/never started, no recording node, or
	// recording-node DTSC output unavailable). For finalized chapters,
	// playback should use vod+<artifact_hash> not dvr+<token>.
	OfflineNotRecorded = "offline:not_recorded"

	// OfflineNotUploaded: VOD artifact doesn't exist (Commodore +
	// chapter-artifact lookup both miss). Persistent.
	OfflineNotUploaded = "offline:not_uploaded"

	// OfflineInvalidToken: dvr+/vod+/processing+ token was empty or
	// malformed. Persistent — token bug at caller.
	OfflineInvalidToken = "offline:invalid_token"

	// OfflineUnavailable: catch-all for "we know about the stream but
	// can't serve it right now" cases that don't fit the above (e.g.,
	// node has no relay base URL advertised). Transient.
	OfflineUnavailable = "offline:unavailable"

	// OfflineNotAuthorized: the artifact's authoritative cluster is not this
	// cluster and is no longer in the tenant's cluster-peer envelope (peer
	// access revoked or never granted). Transient if access is restored.
	OfflineNotAuthorized = "offline:not_authorized"
)
