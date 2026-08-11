package artifacts

import "errors"

// ErrBackendUnattributed is returned when a multipart row carries no recorded backend_id. Under the
// one-immutable-backend-per-cell model every durable row records its owner at creation (and pre-cut in-flight rows are
// adopted from the proven cell identity at boot), so an empty id means the row is not (yet) attributable and must not
// be acted on.
var ErrBackendUnattributed = errors.New("multipart upload has no recorded backend identity")

// ErrBackendForeign is returned when a multipart row's recorded backend_id belongs to a DIFFERENT backend than this
// cell's local store, so this cell does not own the object and must make zero S3 calls against it.
var ErrBackendForeign = errors.New("multipart upload recorded on a foreign backend")

// VerifyLocalMultipartOwnership is the single fail-closed fence every multipart S3 operation — create-retry re-sign,
// completion, abort, completing/aborting recovery, and stale-upload purge — MUST pass BEFORE touching the store. The
// operation may proceed ONLY when the row's recorded backend_id EXACTLY equals this cell's local backend fingerprint.
// An empty recorded id (unattributed) or a mismatch (foreign) returns an error and must result in zero S3 calls, so a
// cell never completes, aborts, or re-signs an upload it does not own — which cleanup would later correctly refuse to
// delete, leaking the object permanently. An empty localBackendID (no local store wired) also fails closed.
func VerifyLocalMultipartOwnership(recordedBackendID, localBackendID string) error {
	if localBackendID == "" || recordedBackendID == "" {
		return ErrBackendUnattributed
	}
	if recordedBackendID != localBackendID {
		return ErrBackendForeign
	}
	return nil
}
