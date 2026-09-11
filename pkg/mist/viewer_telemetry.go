package mist

import (
	"net/url"
	"regexp"
	"strings"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

var telemetryClientSessionID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ViewerTelemetryRequestURL retains only the diagnostic URL and a validated
// attach-scoped fwsid. All other query fields, userinfo and fragments are secret
// by default. Malformed queries cannot supply a correlation identifier.
func ViewerTelemetryRequestURL(raw string) (clientSessionID, sanitized string) {
	if raw == "" {
		return "", ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		prefix := raw
		if index := strings.IndexAny(prefix, "?#"); index >= 0 {
			prefix = prefix[:index]
		}
		parsed, err = url.Parse(prefix)
		if err != nil {
			return "", ""
		}
	}
	if parsed.Opaque != "" {
		return "", ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "", "http", "https", "ws", "wss", "rtmp", "rtmps", "rtsp", "rtsps", "srt", "dtsc", "dtscs", "dtscquic":
	default:
		return "", ""
	}
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	if values := query["fwsid"]; queryErr == nil && len(values) == 1 && telemetryClientSessionID.MatchString(values[0]) {
		clientSessionID = values[0]
	}
	retained := url.Values{}
	if clientSessionID != "" {
		retained.Set("fwsid", clientSessionID)
	}
	parsed.User = nil
	parsed.RawQuery = retained.Encode()
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return clientSessionID, parsed.String()
}

// SanitizeViewerTelemetry removes credentials only from a telemetry copy. The
// caller's original trigger remains available for admission and transport retry.
// Payload identity, not the claimed trigger_type string, selects redaction.
func SanitizeViewerTelemetry(trigger *ipcpb.MistTrigger) *ipcpb.MistTrigger {
	if trigger == nil || (trigger.GetViewerConnect() == nil && trigger.GetPlayRewrite() == nil && trigger.GetConnectionPlay() == nil) {
		return trigger
	}
	clean := proto.CloneOf(trigger)
	if viewer := clean.GetViewerConnect(); viewer != nil {
		viewer.ViewerToken = ""
		_, viewer.RequestUrl = ViewerTelemetryRequestURL(viewer.RequestUrl)
	}
	if rewrite := clean.GetPlayRewrite(); rewrite != nil {
		_, rewrite.RequestUrl = ViewerTelemetryRequestURL(rewrite.RequestUrl)
	}
	if connection := clean.GetConnectionPlay(); connection != nil {
		_, connection.RequestUrl = ViewerTelemetryRequestURL(connection.RequestUrl)
	}
	return clean
}
