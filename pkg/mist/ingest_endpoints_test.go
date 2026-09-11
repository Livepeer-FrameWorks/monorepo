package mist

import (
	"net/url"
	"strings"
	"testing"
)

func TestIngestEndpointTemplateBindsOnlyAtCredentialOwner(t *testing.T) {
	outputs := map[string]any{"HTTP": "https://HOST:8080/$.html", "WebRTC": "ws://HOST:8200/webrtc/$", "RTMP": "rtmps://HOST:2935/view%2Ftenant/play/$", "TSSRT": "srt://HOST:9889?streamid=$"}
	base, key := "https://[2001:db8::1]:8443/view%2Ftenant", "secret/key?&value"
	urls := ResolveIngestURLs(outputs, base, key)
	for protocol, want := range map[string]string{"whip": urls.WHIP, "rtmp": urls.RTMP, "srt": urls.SRT} {
		template := ResolveIngestEndpointTemplate(outputs, base, protocol)
		if template == "" || strings.Contains(template, "secret") || strings.Count(template, "$") != 1 || !ValidIngestEndpointTemplate(template, protocol) {
			t.Fatalf("invalid credential-free %s template: %s", protocol, template)
		}
		if got := BindIngestEndpointTemplate(template, protocol, key); got == "" || got != want {
			t.Fatalf("%s binding differs from direct ingest resolution: got=%s want=%s", protocol, got, want)
		}
	}
}

func TestBindIngestEndpointTemplateRejectsUnconfirmedDestinations(t *testing.T) {
	for _, test := range []struct{ protocol, template string }{
		{"rtmp", "rtmp://HOST:1935/live/$"}, {"rtmp", "rtmp://edge:1935/play/$"},
		{"rtmp", "rtmp://edge:1935/live/fixed"}, {"rtmp", "rtmp://user:secret@edge:1935/live/$"},
		{"rtmp", "rtmp://edge:1935/live/$?extra=1"}, {"rtmp", "rtmp://edge:1935/%24/live/$"},
		{"srt", "srt://edge:9889?streamid=$&extra=1"}, {"whip", "https://edge/webrtc/$#secret"},
		{"rtmp", "https://edge/webrtc/$"}, {"unknown", "https://edge/webrtc/$"},
	} {
		if ValidIngestEndpointTemplate(test.template, test.protocol) || BindIngestEndpointTemplate(test.template, test.protocol, "key") != "" {
			t.Fatalf("unsafe template accepted: %+v", test)
		}
	}
	for _, key := range []string{"", ".", "..", " key", "key\n"} {
		if got := BindIngestEndpointTemplate("rtmp://edge:1935/live/$", "rtmp", key); got != "" {
			t.Fatalf("invalid key accepted: %q", key)
		}
	}
}

func TestResolveIngestURLsUsesExactListenerPortsAndPublicOverrides(t *testing.T) {
	urls := ResolveIngestURLs(map[string]any{
		"WebRTC": "https://whip.example:7443/media/webrtc/$",
		"RTMP":   "rtmps://HOST:2935/play/$",
		"TSSRT":  "srt://srt.example:9889?streamid=$",
	}, "https://[2001:db8::1]:8443/view", "secret/key?&value")
	if urls.WHIP != "https://whip.example:7443/media/webrtc/secret%2Fkey%3F&value" || urls.RTMP != "rtmps://[2001:db8::1]:2935/live/secret%2Fkey%3F&value" {
		t.Fatalf("listener endpoint changed: %+v", urls)
	}
	parsed, err := url.Parse(urls.SRT)
	if err != nil || parsed.Host != "srt.example:9889" || parsed.Query().Get("streamid") != "secret/key?&value" || len(parsed.Query()) != 1 {
		t.Fatalf("SRT stream key escaped incorrectly: %q %v", urls.SRT, err)
	}
}

func TestResolveIngestURLsDoesNotInferMissingProtocols(t *testing.T) {
	urls := ResolveIngestURLs(map[string]any{"HLS": "https://edge.example/hls/$/index.m3u8"}, "https://edge.example", "key")
	if urls != (IngestURLs{}) {
		t.Fatalf("HLS invented ingest: %+v", urls)
	}
	urls = ResolveIngestURLs(map[string]any{"RTMP": "rtmp://HOST:11935/play/$"}, "https://edge.example", "key")
	if urls.RTMP != "rtmp://edge.example:11935/live/key" || urls.WHIP != "" || urls.SRT != "" {
		t.Fatalf("single protocol report expanded: %+v", urls)
	}
}

func TestIngestProtocolValidationNeedsNoCredentialAndPreservesEscapedPrefixes(t *testing.T) {
	outputs := map[string]any{"HTTP": "http://HOST:8080/$.html", "WebRTC": "ws://HOST:8200/webrtc/$", "RTMP": "rtmp://HOST:11935/view%2Ftenant/play/$"}
	for _, protocol := range []string{"whip", "rtmp"} {
		if !SupportsIngestProtocol(outputs, "https://edge/view%2Ftenant", protocol) {
			t.Fatalf("valid %s listener rejected", protocol)
		}
	}
	for _, protocol := range []string{"srt", "WHIP", "unknown"} {
		if SupportsIngestProtocol(outputs, "https://edge", protocol) {
			t.Fatalf("unsupported %s listener inferred", protocol)
		}
	}
	urls := ResolveIngestURLs(outputs, "https://edge/view%2Ftenant", "key")
	if urls.WHIP != "https://edge/view%2Ftenant/webrtc/key" || urls.RTMP != "rtmp://edge:11935/view%2Ftenant/live/key" {
		t.Fatalf("escaped public prefix changed: %+v", urls)
	}
	for _, base := range []string{"https://$host.example", "https://edge/$", "https://edge/%24", "https://edge?private=key", "https://edge#fragment"} {
		if SupportsIngestProtocol(outputs, base, "whip") {
			t.Fatalf("unsafe public base accepted: %q", base)
		}
	}
}

func TestResolveWHIPUsesHTTPListenerNotWebRTCUDPPort(t *testing.T) {
	outputs := map[string]any{"WebRTC": "ws://HOST:18203/webrtc/$", "HTTP": "https://public.example:8443/view/$.html"}
	if urls := ResolveIngestURLs(outputs, "https://other.example", "key"); urls.WHIP != "https://public.example:8443/view/webrtc/key" {
		t.Fatalf("WHIP used UDP listener or lost HTTP public path: %+v", urls)
	}
	delete(outputs, "HTTP")
	if urls := ResolveIngestURLs(outputs, "https://other.example", "key"); urls.WHIP != "" {
		t.Fatalf("WebRTC listener invented HTTP availability: %+v", urls)
	}
	outputs["HTTP"] = "http://HOST:8080/$.html"
	if urls := ResolveIngestURLs(outputs, "https://public.example:8443/view", "key"); urls.WHIP != "https://public.example:8443/view/webrtc/key" {
		t.Fatalf("HOST resolution lost public proxy path: %+v", urls)
	}
}

func TestResolveIngestURLsRejectsMalformedOrAmbiguousTemplates(t *testing.T) {
	for name, outputs := range map[string]map[string]any{
		"credentials":        {"RTMP": "rtmp://user:password@edge:1935/play/$"},
		"wrong_scheme":       {"RTMP": "https://edge:1935/play/$"},
		"missing_key":        {"RTMP": "rtmp://edge:1935/play/fixed"},
		"two_keys":           {"RTMP": "rtmp://edge:1935/$/play/$"},
		"invalid_port":       {"RTMP": "rtmp://edge:99999/play/$"},
		"zero_port":          {"RTMP": "rtmp://edge:0/play/$"},
		"fragment":           {"RTMP": "rtmp://edge:1935/play/$#extra"},
		"empty_query":        {"RTMP": "rtmp://edge:1935/play/$?"},
		"encoded_second_key": {"RTMP": "rtmp://edge:1935/%24/play/$"},
		"control_character":  {"RTMP": "rtmp://edge:1935/play/$\n"},
		"boolean_report":     {"RTMP": map[string]any{"enabled": true}},
		"multiple_addresses": {"RTMP": []any{"rtmp://a/play/$", "rtmp://b/play/$"}},
		"ambiguous_alias":    {"RTMP": "rtmp://a/play/$", "rtmp": "rtmp://b/play/$"},
		"srt_missing_port":   {"TSSRT": "srt://edge?streamid=$"},
		"srt_duplicate_key":  {"TSSRT": "srt://edge:8889?streamid=$&streamid=fixed"},
		"srt_extra_param":    {"TSSRT": "srt://edge:8889?streamid=$&unexpected=value"},
	} {
		t.Run(name, func(t *testing.T) {
			if urls := ResolveIngestURLs(outputs, "https://edge.example", "key"); urls != (IngestURLs{}) {
				t.Fatalf("invalid report accepted: %+v", urls)
			}
		})
	}
}
