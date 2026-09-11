package handlers

import (
	"errors"
	"net/url"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
)

// Clone before adding viewer-specific credentials; endpoint catalogs can be shared
// with other resolutions. Base URLs and metadata are not playback destinations.
func withViewerQueryCredential(response *sharedpb.ViewerEndpointResponse, token string) (*sharedpb.ViewerEndpointResponse, error) {
	if response == nil || response.Primary == nil {
		return nil, errors.New("missing playback destination")
	}
	if token == "" {
		return response, nil
	}
	result, ok := proto.Clone(response).(*sharedpb.ViewerEndpointResponse)
	if !ok {
		return nil, errors.New("invalid playback response")
	}
	stamp := func(raw string) (string, error) {
		if raw == "" {
			return "", nil
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return "", errors.New("invalid playback destination URL")
		}
		switch u.Scheme {
		case "http", "https", "ws", "wss", "rtmp", "rtmps", "rtsp", "rtsps", "srt", "dtsc":
		default:
			return "", errors.New("invalid playback destination scheme")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", errors.New("invalid playback destination query")
		}
		query.Set("jwt", token)
		u.RawQuery = query.Encode()
		return u.String(), nil
	}
	endpoints := append([]*sharedpb.ViewerEndpoint{result.Primary}, result.Fallbacks...)
	for _, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		var err error
		endpoint.Url, err = stamp(endpoint.GetUrl())
		if err != nil {
			return nil, err
		}
		for _, output := range endpoint.GetOutputs() {
			if output == nil {
				continue
			}
			output.Url, err = stamp(output.GetUrl())
			if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}
