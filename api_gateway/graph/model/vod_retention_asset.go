package model

import "time"

// VodRetentionAsset is the node type for the retention-asset picker. It is
// hand-defined (rather than modelgen'd) because the proto package autobinds its
// own periscope.VodRetentionAsset (eligibility + stats only); the GraphQL node
// additionally carries catalog-composed Title/PlaybackID, which the resolver fills
// from Commodore by artifact hash. The gqlgen.yml models override binds this type;
// the Edge/Connection wrappers have no proto collision and are still generated.
type VodRetentionAsset struct {
	ArtifactHash  string    `json:"artifactHash"`
	TotalSessions int       `json:"totalSessions"`
	DurationS     int       `json:"durationS"`
	LastSeen      time.Time `json:"lastSeen"`
	Title         *string   `json:"title,omitempty"`
	PlaybackID    *string   `json:"playbackId,omitempty"`
}
