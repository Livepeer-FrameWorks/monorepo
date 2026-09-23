package handlers

import (
	"errors"
	"net/url"
	"regexp"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
)

var (
	playbackNumberParam = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,6})?$`)
	playbackFileParam   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	playbackTrackParam  = regexp.MustCompile(`^[A-Za-z0-9,!*_.+<>=-]{1,64}$`)
)

// forwardedPlaybackParams are the MistServer output parameters a viewer may put on
// a /play URL: time-range cuts, download naming, pacing, and track selection. Any
// other query parameter stays at Foghorn, so a /play URL cannot set Mist options
// the platform does not document.
var forwardedPlaybackParams = map[string]*regexp.Regexp{
	"startunix": playbackNumberParam,
	"stopunix":  playbackNumberParam,
	"start":     playbackNumberParam,
	"stop":      playbackNumberParam,
	"duration":  playbackNumberParam,
	"rate":      playbackNumberParam,
	"dl":        playbackFileParam,
	"audio":     playbackTrackParam,
	"video":     playbackTrackParam,
	"subtitle":  playbackTrackParam,
}

// viewerForwardedParams returns the allowlisted parameters of a /play request, or
// the name of the first one that is repeated or malformed.
func viewerForwardedParams(query url.Values) (url.Values, string) {
	forwarded := url.Values{}
	for name, pattern := range forwardedPlaybackParams {
		values, ok := query[name]
		if !ok {
			continue
		}
		if len(values) != 1 || !pattern.MatchString(values[0]) {
			return nil, name
		}
		forwarded.Set(name, values[0])
	}
	return forwarded, ""
}

// Clone before adding viewer-specific parameters; endpoint catalogs can be shared
// with other resolutions. Base URLs and metadata are not playback destinations.
func withViewerQueryParams(response *sharedpb.ViewerEndpointResponse, params url.Values) (*sharedpb.ViewerEndpointResponse, error) {
	if len(params) == 0 {
		return response, nil
	}
	if response == nil || response.Primary == nil {
		return nil, errors.New("missing playback destination")
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
		for name, values := range params {
			query[name] = append([]string(nil), values...)
		}
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
