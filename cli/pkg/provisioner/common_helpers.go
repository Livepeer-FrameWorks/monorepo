package provisioner

import "fmt"

// Cross-file helpers shared by the role-backed provisioners and the edge
// provisioner: release-channel lookup and small set utilities.

// platformChannelFromMetadata pulls the release-channel key that the
// cluster-provision flow injects into every ServiceConfig.Metadata. Empty
// string tells the resolver to use the default channel.
func platformChannelFromMetadata(metadata map[string]any) string {
	if v, ok := metadata["platform_channel"].(string); ok {
		return v
	}
	return ""
}

func resolvePinnedImage(serviceName string, config ServiceConfig) (string, error) {
	if config.Image != "" {
		return config.Image, nil
	}
	image, err := imageFromReleaseManifest(serviceName, config.Version, config.Metadata)
	if err != nil {
		return "", fmt.Errorf("resolve %s image: %w", serviceName, err)
	}
	return image, nil
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
