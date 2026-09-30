package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestRestreamTrackParamsPlatformCapsAndModes(t *testing.T) {
	kick := restreamTrackParams("kick", "AUTO")
	if kick.Video != "restream_auto" || kick.MaxVideoBPS != 8_000_000 || kick.MaxVideoHeight != 1080 || kick.MaxVideoFPS != 60 || kick.MaxAudioRate != 48_000 {
		t.Fatalf("kick caps = %+v", kick)
	}
	x := restreamTrackParams("x", "SOURCE_VIDEO")
	if x.Video != "restream_source" || x.MaxVideoBPS != 12_000_000 || x.MaxAudioBPS != 0 {
		t.Fatalf("x caps = %+v", x)
	}
	twitch := restreamTrackParams("twitch", "PROCESSED_VIDEO")
	if twitch.Video != "restream_processed" || twitch.MaxVideoBPS != 0 || twitch.MaxVideoHeight != 0 {
		t.Fatalf("twitch must not enforce recommendation-only caps: %+v", twitch)
	}
}

func TestFindDesiredRestreamPushSkipsOldSameURIPolicy(t *testing.T) {
	target := restreamTarget{targetURI: "rtmp://example.test/live/key", videoChoice: "SOURCE_VIDEO"}
	pushes := []mist.PushInfo{
		{ID: 7, StreamName: "live+a", TargetURI: target.targetURI, Params: map[string]interface{}{"video": "restream_auto"}},
		{ID: 8, StreamName: "live+a", TargetURI: target.targetURI, Params: map[string]interface{}{"video": "restream_source"}},
	}
	push, found := findDesiredRestreamPush(pushes, "live+a", target)
	if !found || push.ID != 8 {
		t.Fatalf("desired replacement push = %+v, found=%v", push, found)
	}
	if _, found := findDesiredRestreamPush(pushes[:1], "live+a", target); found {
		t.Fatal("old policy must not confirm the new target revision")
	}
}

func TestFindDesiredRestreamPushChecksUppercaseRTMPPolicy(t *testing.T) {
	target := restreamTarget{targetURI: "RTMP://example.test/live/key", videoChoice: "SOURCE_VIDEO"}
	push := mist.PushInfo{ID: 7, StreamName: "live+a", TargetURI: target.targetURI}
	if _, found := findDesiredRestreamPush([]mist.PushInfo{push}, "live+a", target); found {
		t.Fatal("uppercase RTMP target bypassed video selection policy")
	}
}
