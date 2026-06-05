package tools

import "testing"

// classifyProtocol decides which packet-loss threshold table applies, so a
// misclassification silently grades a realtime stream against the laxer
// streaming thresholds. Pin the substring + case-insensitive rules.
func TestClassifyProtocol(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"WebRTC", protocolTypeRealtime},
		{"srt://host:9000", protocolTypeRealtime},
		{"rtp", protocolTypeRealtime},
		{"raw-udp", protocolTypeRealtime},
		{"HLS", protocolTypeStreaming},
		{"dash", protocolTypeStreaming},
		{"rtmp://ingest", protocolTypeStreaming},
		{"https-llhls", protocolTypeStreaming},
		{"", protocolTypeUnknown},
		{"quic", protocolTypeUnknown},
	}
	for _, tt := range tests {
		if got := classifyProtocol(tt.in); got != tt.want {
			t.Errorf("classifyProtocol(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// packetLossStatus must apply the realtime table strictly: 1% loss is only a
// warning for streaming but already critical for realtime. The boundaries are
// inclusive (<=), so test exactly on the thresholds.
func TestPacketLossStatusThresholdsDifferByClass(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		loss     float64
		want     string
	}{
		{"realtime healthy boundary", protocolTypeRealtime, packetLossRealtimeHealthy, "healthy"},
		{"realtime warning boundary", protocolTypeRealtime, packetLossRealtimeWarning, "warning"},
		{"realtime critical above warning", protocolTypeRealtime, packetLossRealtimeWarning + 0.0001, "critical"},
		{"streaming healthy boundary", protocolTypeStreaming, packetLossStreamingHealthy, "healthy"},
		{"streaming warning boundary", protocolTypeStreaming, packetLossStreamingWarning, "warning"},
		{"streaming critical above warning", protocolTypeStreaming, packetLossStreamingWarning + 0.0001, "critical"},
		// 1% loss: only a warning for realtime but still healthy for streaming.
		{"one percent is realtime-warning", protocolTypeRealtime, 0.01, "warning"},
		{"one percent is streaming-healthy", protocolTypeStreaming, 0.01, "healthy"},
		// 2% loss: critical for realtime, still only a warning for streaming.
		{"two percent is realtime-critical", protocolTypeRealtime, 0.02, "critical"},
		{"two percent is streaming-warning", protocolTypeStreaming, 0.02, "warning"},
		// Unknown protocol falls back to the streaming table, not realtime.
		{"unknown uses streaming table", protocolTypeUnknown, 0.03, "warning"},
	}
	for _, tt := range tests {
		if got := packetLossStatus(tt.protocol, tt.loss); got != tt.want {
			t.Errorf("%s: packetLossStatus(%q, %v) = %q, want %q", tt.name, tt.protocol, tt.loss, got, tt.want)
		}
	}
}
