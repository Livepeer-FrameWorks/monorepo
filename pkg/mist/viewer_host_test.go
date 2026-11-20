package mist

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"testing"
)

func TestViewerHostsAreCanonicalAcrossTriggerIntakes(t *testing.T) {
	const expanded = "2001:0db8:0000:0000:0000:0000:0000:0001"
	const canonical = "2001:db8::1"
	cases := []struct {
		name string
		kind TriggerType
		body string
	}{
		{"rewrite", TriggerPlayRewrite, "stream\n" + expanded + "\nHLS\nhttp://edge/hls/stream/index.m3u8"},
		{"connect", TriggerUserNew, "stream\n" + expanded + "\ntoken\nHLS\nhttp://edge/hls/stream/index.m3u8\nsession"},
		{"disconnect", TriggerUserEnd, "session\nstream\nHLS\n" + expanded + "\n10\n100\n200\n0"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			trigger, err := ParseTriggerToProtobuf(test.kind, []byte(test.body), "node", logging.NewLogger())
			if err != nil {
				t.Fatal(err)
			}
			host := ""
			switch test.kind {
			case TriggerPlayRewrite:
				host = trigger.GetPlayRewrite().GetViewerHost()
			case TriggerUserNew:
				host = trigger.GetViewerConnect().GetHost()
			case TriggerUserEnd:
				host = trigger.GetViewerDisconnect().GetHost()
			}
			if host != canonical {
				t.Fatalf("host %q, want %q", host, canonical)
			}
		})
	}
}
