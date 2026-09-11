package handlers

import (
	"strings"

	"frameworks/api_balancing/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func requestedViewerPlacementProtocol(raw string) (string, error) {
	protocol := normalizeProtocol(raw)
	if protocol == "any" {
		return "", nil
	}
	if strings.EqualFold(raw, "whep") {
		protocol = "whep"
	}
	if strings.EqualFold(raw, "html") || strings.EqualFold(raw, "embed") {
		protocol = "mist_html"
	}
	if canonical := mist.PlaybackProtocol(protocol); canonical != "" {
		return canonical, nil
	}
	return "", control.ErrInvalidViewerProtocol
}

func requestedViewerManifestProtocol(raw, manifest string) (string, error) {
	protocol, err := requestedViewerPlacementProtocol(raw)
	if err != nil || manifest == "" {
		return protocol, err
	}
	if strings.ContainsAny(manifest, "\\?#") || strings.HasPrefix(manifest, "/") {
		return "", control.ErrInvalidViewerProtocol
	}
	for _, segment := range strings.Split(manifest, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", control.ErrInvalidViewerProtocol
		}
	}
	format := ""
	switch {
	case strings.HasSuffix(manifest, ".mpd"):
		format = "dash"
	case strings.HasSuffix(manifest, ".m3u8"):
		format = "hls"
		if protocol == "cmaf" {
			format = "cmaf"
		}
	case strings.HasSuffix(manifest, ".f4m"):
		format = "hds"
	case manifest == "Manifest" || strings.HasSuffix(manifest, "/Manifest"):
		format = "smoothstreaming"
	}
	// CMAF carries both DASH and HLS manifests. The requested manifest, not
	// the shared container path, determines which listener URL is prepared.
	compatible := protocol == "" || protocol == format || (protocol == "cmaf" && (format == "dash" || format == "smoothstreaming"))
	if format == "" || !compatible {
		return "", control.ErrInvalidViewerProtocol
	}
	return format, nil
}

var viewerPlacementPreparer control.ViewerPlacementPreparer
var storedMediaPlacementPermitter control.ViewerPlacementPermitter
var storedMediaPlacementRequired bool

// SetViewerPlacementPreparer installs the policy path before listeners start.
// Clearing an installed preparer cannot restore legacy candidate selection.
func SetViewerPlacementPreparer(preparer control.ViewerPlacementPreparer) {
	viewerPlacementPreparer = preparer
}

// SetStoredMediaPlacementPermitter installs serving-policy enforcement for
// artifact playback before listeners start. Stored media keeps its storage
// ranking; this only removes destinations the tenant's policy refuses.
// Calling it requires placement from then on; clearing the permitter refuses
// stored media rather than restoring unfiltered storage candidates.
func SetStoredMediaPlacementPermitter(permitter control.ViewerPlacementPermitter) {
	storedMediaPlacementRequired = true
	storedMediaPlacementPermitter = permitter
}
