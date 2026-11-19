package handlers

import (
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// A prepared primary URL can replace a catalog entry only for its own format.
// Otherwise a missing MP4 output could redirect to HLS or an HTML player page.
func resolvedViewerProtocolURL(endpoint *sharedpb.ViewerEndpoint, protocol string) string {
	if endpoint == nil {
		return ""
	}
	if raw := findProtocolURL(endpoint.Outputs, protocol); raw != "" {
		return raw
	}
	requested := mist.PlaybackProtocol(protocol)
	if requested != "" {
		for name, output := range endpoint.Outputs {
			if output != nil && mist.PlaybackProtocol(name) == requested {
				return output.GetUrl()
			}
		}
	}
	if strings.EqualFold(endpoint.GetProtocol(), protocol) || requested != "" && mist.PlaybackProtocol(endpoint.GetProtocol()) == requested {
		return endpoint.GetUrl()
	}
	return ""
}
