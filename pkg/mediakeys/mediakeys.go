// Package mediakeys holds object-store keys shared between the media plane (Foghorn, which WRITES) and the asset
// server (Chandler, which READS), so the two can never drift. Both callers prepend the cell's configured S3 prefix.
package mediakeys

// ReadinessSentinelKey is the object under the served thumbnails/ namespace that Foghorn writes at boot and Chandler
// reads to prove it can GetObject from the backend — a real, provisioned object, so an absent-or-denied response can
// never masquerade as ready, and reading it needs only s3:GetObject (no ListBucket). Foghorn's PutObject and
// Chandler's GetObject both prepend the cell S3 prefix, so the effective key is {prefix}/thumbnails/.readiness.
const ReadinessSentinelKey = "thumbnails/.readiness"
