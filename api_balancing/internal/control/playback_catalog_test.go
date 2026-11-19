package control

import "testing"

func TestViewerCatalogDoesNotInventProgressiveSupport(t *testing.T) {
	endpoint := BuildViewerEndpointFromOutputs("node", &NodeOutputs{
		BaseURL: "https://edge/view",
		Outputs: map[string]any{
			"HTTP": "https://edge/view/$.html",
			"HLS":  "https://edge/view/hls/$/index.m3u8",
		},
	}, "live+source", true)
	if endpoint == nil {
		t.Fatal("observed HLS must remain playable")
	}
	for _, protocol := range []string{"MP4", "MEWS", "WEBM", "DASH"} {
		if endpoint.Outputs[protocol] != nil {
			t.Errorf("HTTP listener invented %s support", protocol)
		}
	}
	if endpoint.Outputs["PLAYER_JS"].GetUrl() != "https://edge/view/player.js" {
		t.Fatal("observed HTTP listener lost its player script")
	}
}
