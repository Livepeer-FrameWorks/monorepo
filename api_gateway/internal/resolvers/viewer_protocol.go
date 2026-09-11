package resolvers

import (
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func viewerProtocolRequirement(requested string) (string, error) {
	switch requested {
	case "":
		return "", nil
	case "WEBRTC", "WHEP", "HLS", "DASH", "HLS_CMAF", "MEWS", "MEWS_WEBM", "MP4", "WEBM", "MKV", "TS", "AAC", "H264", "H264_WS", "RAW_WS", "JSON_WS", "FLV", "HDS", "SMOOTHSTREAMING", "SDP", "MIST_HTML", "RTMP", "RTSP", "SRT", "DTSC":
		return mist.PlaybackProtocol(requested), nil
	default:
		return "", fmt.Errorf("unsupported viewer protocol")
	}
}

func viewerDemoForProtocol(response *sharedpb.ViewerEndpointResponse, protocol string) (*sharedpb.ViewerEndpointResponse, error) {
	if protocol == "" {
		return response, nil
	}
	var matches []*sharedpb.ViewerEndpoint
	for _, endpoint := range append([]*sharedpb.ViewerEndpoint{response.Primary}, response.Fallbacks...) {
		if endpoint == nil {
			continue
		}
		for key, output := range endpoint.Outputs {
			if output != nil && mist.PlaybackProtocol(key) == protocol {
				endpoint.Protocol, endpoint.Url = protocol, output.Url
				endpoint.Outputs = map[string]*sharedpb.OutputEndpoint{key: output}
				matches = append(matches, endpoint)
				break
			}
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("requested viewer protocol is unavailable in demo")
	}
	response.Primary, response.Fallbacks = matches[0], matches[1:]
	return response, nil
}
