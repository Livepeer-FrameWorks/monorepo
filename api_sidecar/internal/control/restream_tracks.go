package control

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func restreamTrackParams(platform, videoChoice string) mist.PushTrackParams {
	params := mist.PushTrackParams{Video: "restream_auto"}
	switch videoChoice {
	case "SOURCE_VIDEO":
		params.Video = "restream_source"
	case "PROCESSED_VIDEO":
		params.Video = "restream_processed"
	}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "kick":
		params.VideoCodecs = []string{"H264"}
		params.MaxVideoWidth = 1920
		params.MaxVideoHeight = 1080
		params.MaxVideoFPS = 60
		params.MaxVideoBPS = 8_000_000
		params.AudioCodecs = []string{"AAC"}
		params.MaxAudioChannels = 2
		params.MaxAudioRate = 48_000
	case "x":
		params.VideoCodecs = []string{"H264"}
		params.MaxVideoBPS = 12_000_000
		params.AudioCodecs = []string{"AAC"}
	}
	return params
}

func restreamIsRTMPURI(uri string) bool {
	uri = strings.ToLower(strings.TrimSpace(uri))
	return strings.HasPrefix(uri, "rtmp://") || strings.HasPrefix(uri, "rtmps://")
}

func restreamPushParamsMatch(actual map[string]interface{}, desired mist.PushTrackParams) bool {
	encoded, err := json.Marshal(desired)
	if err != nil {
		return false
	}
	var expected map[string]interface{}
	if err := json.Unmarshal(encoded, &expected); err != nil {
		return false
	}
	return reflect.DeepEqual(actual, expected)
}

func findDesiredRestreamPush(pushes []mist.PushInfo, streamName string, target restreamTarget, legacyAuto ...bool) (mist.PushInfo, bool) {
	for _, push := range pushes {
		if push.StreamName != streamName || strings.TrimSpace(push.TargetURI) != target.targetURI {
			continue
		}
		if restreamIsRTMPURI(target.targetURI) {
			allowLegacy := len(legacyAuto) > 0 && legacyAuto[0] && target.videoChoice == "AUTO" && len(push.Params) == 0
			if !allowLegacy && !restreamPushParamsMatch(push.Params, restreamTrackParams(target.platform, target.videoChoice)) {
				continue
			}
		}
		return push, true
	}
	return mist.PushInfo{}, false
}
