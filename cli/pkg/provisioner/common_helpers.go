package provisioner

// Cross-file helpers shared by the role-backed provisioners and the edge
// provisioner: release-channel lookup, small set utilities, default image
// pins for services whose manifest entry doesn't override them.

const (
	defaultChatwootImage = "chatwoot/chatwoot:latest"
	defaultListmonkImage = "listmonk/listmonk:v4.0.1"
)

// platformChannelFromMetadata pulls the release-channel key that the
// cluster-provision flow injects into every ServiceConfig.Metadata. Empty
// string tells the resolver to use the default channel.
func platformChannelFromMetadata(metadata map[string]any) string {
	if v, ok := metadata["platform_channel"].(string); ok {
		return v
	}
	return ""
}

// dbSet turns a database name list into a set for O(1) membership checks.
// Used by the seed helpers to skip databases not present in the manifest.
func dbSet(names []string) map[string]struct{} {
	s := make(map[string]struct{}, len(names))
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}

// orElse returns v when non-empty, otherwise fallback. Read as "v or else
// fallback"; used by the role vars builders to pick manifest-provided
// values over defaults without the verbose `if v := ...; v != ""` ladder.
func orElse(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
