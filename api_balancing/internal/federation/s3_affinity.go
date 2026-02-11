package federation

import (
	"net/url"
	"strings"
)

// ClusterS3Config holds S3 configuration for a cluster, used to determine
// whether two clusters share the same S3 bucket (same-bucket affinity).
type ClusterS3Config struct {
	ClusterID  string
	S3Bucket   string
	S3Endpoint string
	S3Region   string
}

// IsSameBucket returns true if both clusters use the same S3 bucket and endpoint.
// Same-bucket means the local Foghorn can generate presigned URLs directly
// without calling PrepareArtifact on the origin — avoiding a round-trip.
func IsSameBucket(local, remote ClusterS3Config) bool {
	if local.S3Bucket == "" || remote.S3Bucket == "" {
		return false
	}
	return local.S3Bucket == remote.S3Bucket &&
		normalizeEndpoint(local.S3Endpoint) == normalizeEndpoint(remote.S3Endpoint)
}

// normalizeEndpoint strips protocol, trailing slashes, and default ports
// so that "https://s3.us-east-1.amazonaws.com/" and "s3.us-east-1.amazonaws.com"
// compare as equal.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return strings.ToLower(endpoint)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "443" || port == "80" || port == "" {
		return host
	}
	return host + ":" + port
}
