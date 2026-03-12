package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// --- convertStreamAPIToMistTrigger ---

func TestConvertStreamAPI_FullPayload(t *testing.T) {
	streamData := map[string]interface{}{
		"viewers":     float64(42),
		"inputs":      float64(1),
		"upbytes":     float64(1024000),
		"downbytes":   float64(8192000),
		"replicated":  true,
		"packsent":    float64(10000),
		"packloss":    float64(5),
		"packretrans": float64(3),
		"viewseconds": float64(12345),
	}
	healthData := map[string]interface{}{
		"buffer":      float64(500),
		"jitter":      float64(10),
		"maxkeepaway": float64(2000),
		"issues":      "HLSnoaudio!",
	}
	trackDetails := []map[string]interface{}{
		{
			"type":         "video",
			"width":        1920,
			"height":       1080,
			"fps":          float64(30),
			"bitrate_kbps": 6000,
			"codec":        "H264",
			"buffer":       200,
			"jitter":       5,
		},
		{
			"type":         "audio",
			"channels":     2,
			"sample_rate":  48000,
			"codec":        "AAC",
			"bitrate_kbps": 128,
		},
	}

	trigger := convertStreamAPIToMistTrigger("node-1", "live+test", "test", streamData, healthData, trackDetails, 2, logging.NewLogger())

	if trigger.TriggerType != "STREAM_LIFECYCLE_UPDATE" {
		t.Fatalf("expected STREAM_LIFECYCLE_UPDATE, got %s", trigger.TriggerType)
	}
	if trigger.NodeId != "node-1" {
		t.Fatalf("expected node-1, got %s", trigger.NodeId)
	}

	slu := trigger.GetStreamLifecycleUpdate()
	if slu == nil {
		t.Fatal("expected StreamLifecycleUpdate payload")
	}
	if slu.InternalName != "test" {
		t.Fatalf("expected internal name 'test', got %q", slu.InternalName)
	}
	if slu.GetTotalViewers() != 42 {
		t.Fatalf("expected 42 viewers, got %d", slu.GetTotalViewers())
	}
	if slu.GetTotalInputs() != 1 {
		t.Fatalf("expected 1 input, got %d", slu.GetTotalInputs())
	}
	if slu.GetUploadedBytes() != 1024000 {
		t.Fatalf("expected 1024000 uploaded bytes, got %d", slu.GetUploadedBytes())
	}
	if slu.GetDownloadedBytes() != 8192000 {
		t.Fatalf("expected 8192000 downloaded bytes, got %d", slu.GetDownloadedBytes())
	}
	if !slu.GetReplicated() {
		t.Fatal("expected replicated=true")
	}
	if slu.GetPacketsSent() != 10000 {
		t.Fatalf("expected 10000 packets sent, got %d", slu.GetPacketsSent())
	}
	if slu.GetPacketsLost() != 5 {
		t.Fatalf("expected 5 packets lost, got %d", slu.GetPacketsLost())
	}
	if slu.GetPacketsRetransmitted() != 3 {
		t.Fatalf("expected 3 retransmitted, got %d", slu.GetPacketsRetransmitted())
	}
	if slu.GetViewerSeconds() != 12345 {
		t.Fatalf("expected 12345 viewer seconds, got %d", slu.GetViewerSeconds())
	}
	if slu.GetBufferMs() != 500 {
		t.Fatalf("expected 500 buffer ms, got %d", slu.GetBufferMs())
	}
	if slu.GetJitterMs() != 10 {
		t.Fatalf("expected 10 jitter ms, got %d", slu.GetJitterMs())
	}
	if slu.GetMaxKeepawayMs() != 2000 {
		t.Fatalf("expected 2000 max keepaway, got %d", slu.GetMaxKeepawayMs())
	}
	if !slu.GetHasIssues() {
		t.Fatal("expected has_issues=true")
	}

	// Quality tier should be "1080p30 H264 @ 6.0Mbps"
	qt := slu.GetQualityTier()
	if qt != "1080p30 H264 @ 6.0Mbps" {
		t.Fatalf("expected quality tier '1080p30 H264 @ 6.0Mbps', got %q", qt)
	}
	if slu.GetPrimaryWidth() != 1920 {
		t.Fatalf("expected primary width 1920, got %d", slu.GetPrimaryWidth())
	}
	if slu.GetPrimaryHeight() != 1080 {
		t.Fatalf("expected primary height 1080, got %d", slu.GetPrimaryHeight())
	}
	if slu.GetAudioChannels() != 2 {
		t.Fatalf("expected 2 audio channels, got %d", slu.GetAudioChannels())
	}
	if slu.GetAudioSampleRate() != 48000 {
		t.Fatalf("expected 48000 sample rate, got %d", slu.GetAudioSampleRate())
	}
	if slu.GetTrackCount() != 2 {
		t.Fatalf("expected track count 2, got %d", slu.GetTrackCount())
	}
}

func TestConvertStreamAPI_MinimalPayload(t *testing.T) {
	trigger := convertStreamAPIToMistTrigger("node-1", "live+test", "test", nil, nil, nil, 0, logging.NewLogger())

	slu := trigger.GetStreamLifecycleUpdate()
	if slu == nil {
		t.Fatal("expected StreamLifecycleUpdate payload")
	}
	if slu.Status != "live" {
		t.Fatalf("expected status 'live', got %q", slu.Status)
	}
	if slu.TotalViewers != nil {
		t.Fatalf("expected nil viewers, got %d", slu.GetTotalViewers())
	}
	if slu.QualityTier != nil {
		t.Fatalf("expected nil quality tier, got %q", slu.GetQualityTier())
	}
	if slu.GetHasIssues() {
		t.Fatal("expected no issues for nil data")
	}
}

func TestConvertStreamAPI_TrackDetails(t *testing.T) {
	streamData := map[string]interface{}{
		"packsent": float64(1000),
		"packloss": float64(100), // 10% loss — should trigger "High packet loss"
	}
	trackDetails := []map[string]interface{}{
		{
			"type":   "video",
			"height": 720,
			"fps":    float64(60),
			"codec":  "VP9",
			"jitter": 150, // High jitter
			"buffer": 30,  // Low buffer
		},
		{
			"type":   "video",
			"height": 480,
			"fps":    float64(30),
		},
		{
			"type":        "audio",
			"channels":    6,
			"sample_rate": 44100,
			"codec":       "OPUS",
		},
	}

	trigger := convertStreamAPIToMistTrigger("node-2", "live+multi", "multi", streamData, nil, trackDetails, 3, logging.NewLogger())
	slu := trigger.GetStreamLifecycleUpdate()

	// Should pick first video track (720p60)
	if slu.GetQualityTier() != "720p60 VP9" {
		t.Fatalf("expected '720p60 VP9', got %q", slu.GetQualityTier())
	}
	// First audio track
	if slu.GetAudioChannels() != 6 {
		t.Fatalf("expected 6 channels, got %d", slu.GetAudioChannels())
	}
	// Issues: high packet loss + high jitter + low buffer
	if !slu.GetHasIssues() {
		t.Fatal("expected issues from packet loss + jitter + buffer")
	}
	desc := slu.GetIssuesDescription()
	if desc == "" {
		t.Fatal("expected non-empty issues description")
	}
}

func TestConvertStreamAPI_ResolutionTiers(t *testing.T) {
	tests := []struct {
		height int
		fps    float64
		want   string
	}{
		{2160, 60, "2160p60"},
		{1440, 30, "1440p30"},
		{1080, 0, "1080p"},
		{720, 24, "720p24"},
		{480, 30, "480p30"},
		{360, 0, "SD"},
	}

	for _, tt := range tests {
		streamData := map[string]interface{}{}
		tracks := []map[string]interface{}{
			{"type": "video", "height": tt.height, "fps": tt.fps},
		}
		trigger := convertStreamAPIToMistTrigger("n", "s", "s", streamData, nil, tracks, 1, logging.NewLogger())
		got := trigger.GetStreamLifecycleUpdate().GetQualityTier()
		if got != tt.want {
			t.Errorf("height=%d fps=%v: expected %q, got %q", tt.height, tt.fps, tt.want, got)
		}
	}
}

// --- convertClientAPIToMistTrigger ---

func TestConvertClientAPI_FullPayload(t *testing.T) {
	trigger := convertClientAPIToMistTrigger(
		"node-1", "live+stream", "stream",
		"HLS", "1.2.3.4", "session-abc",
		120.5, 45.2,
		512000, 2048000,
		100000, 50000, 9500, 50, 10,
		logging.NewLogger(),
	)

	if trigger.TriggerType != "CLIENT_LIFECYCLE_UPDATE" {
		t.Fatalf("expected CLIENT_LIFECYCLE_UPDATE, got %s", trigger.TriggerType)
	}

	clu := trigger.GetClientLifecycleUpdate()
	if clu == nil {
		t.Fatal("expected ClientLifecycleUpdate payload")
	}
	if clu.NodeId != "node-1" {
		t.Fatalf("expected node-1, got %s", clu.NodeId)
	}
	if clu.InternalName != "stream" {
		t.Fatalf("expected internal name 'stream', got %q", clu.InternalName)
	}
	if clu.Protocol != "HLS" {
		t.Fatalf("expected protocol HLS, got %s", clu.Protocol)
	}
	if clu.Host != "1.2.3.4" {
		t.Fatalf("expected host 1.2.3.4, got %s", clu.Host)
	}
	if clu.GetSessionId() != "session-abc" {
		t.Fatalf("expected session ID, got %q", clu.GetSessionId())
	}
	if clu.GetConnectionTime() != float32(120.5) {
		t.Fatalf("expected connection time 120.5, got %v", clu.GetConnectionTime())
	}
	if clu.GetPosition() != float32(45.2) {
		t.Fatalf("expected position 45.2, got %v", clu.GetPosition())
	}
	if clu.GetBandwidthInBps() != 512000 {
		t.Fatalf("expected bandwidth in 512000, got %d", clu.GetBandwidthInBps())
	}
	if clu.GetBandwidthOutBps() != 2048000 {
		t.Fatalf("expected bandwidth out 2048000, got %d", clu.GetBandwidthOutBps())
	}
	if clu.GetBytesDownloaded() != 100000 {
		t.Fatalf("expected 100000 bytes down, got %d", clu.GetBytesDownloaded())
	}
	if clu.GetBytesUploaded() != 50000 {
		t.Fatalf("expected 50000 bytes up, got %d", clu.GetBytesUploaded())
	}
	if clu.GetPacketsSent() != 9500 {
		t.Fatalf("expected 9500 packets sent, got %d", clu.GetPacketsSent())
	}
	if clu.GetPacketsLost() != 50 {
		t.Fatalf("expected 50 packets lost, got %d", clu.GetPacketsLost())
	}
	if clu.GetPacketsRetransmitted() != 10 {
		t.Fatalf("expected 10 retransmitted, got %d", clu.GetPacketsRetransmitted())
	}
}

func TestConvertClientAPI_ZeroValues(t *testing.T) {
	trigger := convertClientAPIToMistTrigger(
		"node-1", "live+s", "s",
		"RTMP", "0.0.0.0", "",
		0, 0,
		0, 0,
		0, 0, 0, 0, 0,
		logging.NewLogger(),
	)

	clu := trigger.GetClientLifecycleUpdate()
	if clu.Action != "connect" {
		t.Fatalf("expected action 'connect', got %q", clu.Action)
	}
	if clu.SessionId != nil {
		t.Fatalf("expected nil session ID for empty string, got %q", clu.GetSessionId())
	}
	if clu.GetBandwidthInBps() != 0 {
		t.Fatalf("expected 0 bandwidth, got %d", clu.GetBandwidthInBps())
	}
	if clu.GetPacketsSent() != 0 {
		t.Fatalf("expected 0 packets sent, got %d", clu.GetPacketsSent())
	}
}

// --- convertNodeAPIToMistTrigger ---

func TestConvertNodeAPI_FullPayload(t *testing.T) {
	pm := &PrometheusMonitor{
		edgePublicURL: "https://edge.example.com",
		baseURL:       "http://mist.internal:4242",
		lastBwUp:      1000,
		lastBwDown:    5000,
		lastPollTime:  time.Now().Add(-5 * time.Second),
	}

	jsonData := map[string]interface{}{
		"cpu":       float64(45),
		"mem_total": float64(8589934592),
		"mem_used":  float64(4294967296),
		"shm_total": float64(1073741824),
		"shm_used":  float64(536870912),
		"bw":        []interface{}{float64(2000), float64(10000)},
		"curr":      []interface{}{float64(10), float64(2), float64(5), float64(0), float64(3)},
		"bwlimit":   float64(134217728),
		"loc": map[string]interface{}{
			"lat":  float64(37.7749),
			"lon":  float64(-122.4194),
			"name": "San Francisco",
		},
		"triggers": map[string]interface{}{
			"PUSH_END": map[string]interface{}{"count": float64(5)},
		},
		"outputs": map[string]interface{}{
			"HLS": map[string]interface{}{"enabled": true},
		},
		"streams": map[string]interface{}{
			"live+demo_stream": map[string]interface{}{
				"curr": []interface{}{float64(5), float64(1)},
				"bw":   []interface{}{float64(100), float64(500)},
				"rep":  true,
			},
		},
	}

	trigger := pm.convertNodeAPIToMistTrigger("node-1", jsonData, logging.NewLogger())

	if trigger.TriggerType != "NODE_LIFECYCLE_UPDATE" {
		t.Fatalf("expected NODE_LIFECYCLE_UPDATE, got %s", trigger.TriggerType)
	}

	nlu := trigger.GetNodeLifecycleUpdate()
	if nlu == nil {
		t.Fatal("expected NodeLifecycleUpdate payload")
	}
	if nlu.BaseUrl != "https://edge.example.com" {
		t.Fatalf("expected public edge URL, got %q", nlu.BaseUrl)
	}
	if nlu.CpuTenths != 450 {
		t.Fatalf("expected CPU tenths 450, got %d", nlu.CpuTenths)
	}
	if nlu.RamMax != 8589934592 {
		t.Fatalf("expected RAM max 8GB, got %d", nlu.RamMax)
	}
	if nlu.RamCurrent != 4294967296 {
		t.Fatalf("expected RAM current 4GB, got %d", nlu.RamCurrent)
	}
	if nlu.ShmTotalBytes != 1073741824 {
		t.Fatalf("expected SHM total 1GB, got %d", nlu.ShmTotalBytes)
	}
	if nlu.ShmUsedBytes != 536870912 {
		t.Fatalf("expected SHM used 512MB, got %d", nlu.ShmUsedBytes)
	}
	if nlu.ConnectionsCurrent != 10 {
		t.Fatalf("expected 10 current connections, got %d", nlu.ConnectionsCurrent)
	}
	if nlu.ConnectionsInputs != 2 {
		t.Fatalf("expected 2 inputs, got %d", nlu.ConnectionsInputs)
	}
	if nlu.ConnectionsOutgoing != 5 {
		t.Fatalf("expected 5 outgoing, got %d", nlu.ConnectionsOutgoing)
	}
	if nlu.ConnectionsCached != 3 {
		t.Fatalf("expected 3 cached, got %d", nlu.ConnectionsCached)
	}
	if nlu.BwLimit != 134217728 {
		t.Fatalf("expected bwlimit 128MB, got %d", nlu.BwLimit)
	}
	if nlu.Latitude != 37.7749 {
		t.Fatalf("expected latitude 37.7749, got %f", nlu.Latitude)
	}
	if nlu.Longitude != -122.4194 {
		t.Fatalf("expected longitude -122.4194, got %f", nlu.Longitude)
	}
	if nlu.Location != "San Francisco" {
		t.Fatalf("expected location 'San Francisco', got %q", nlu.Location)
	}
	if nlu.TriggersJson == "" {
		t.Fatal("expected triggers JSON to be populated")
	}
	if nlu.OutputsJson == "" {
		t.Fatal("expected outputs JSON to be populated")
	}
	if !nlu.IsHealthy {
		t.Fatal("expected node to be healthy (all metrics below 90%)")
	}
	// Bandwidth rate from delta
	if nlu.BandwidthOutTotal != 2000 {
		t.Fatalf("expected bw out total 2000, got %d", nlu.BandwidthOutTotal)
	}
	if nlu.BandwidthInTotal != 10000 {
		t.Fatalf("expected bw in total 10000, got %d", nlu.BandwidthInTotal)
	}
	if nlu.UpSpeed == 0 {
		t.Fatal("expected non-zero up speed from delta calculation")
	}
	// Streams map
	if len(nlu.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(nlu.Streams))
	}
	if sd, ok := nlu.Streams["demo_stream"]; !ok {
		t.Fatal("expected stream key 'demo_stream' (normalized)")
	} else {
		if sd.Total != 5 {
			t.Fatalf("expected 5 viewers, got %d", sd.Total)
		}
		if sd.Inputs != 1 {
			t.Fatalf("expected 1 input, got %d", sd.Inputs)
		}
		if !sd.Replicated {
			t.Fatal("expected replicated=true")
		}
	}
	if nlu.ActiveStreams != 1 {
		t.Fatalf("expected 1 active stream, got %d", nlu.ActiveStreams)
	}
}

func TestConvertNodeAPI_MissingFields(t *testing.T) {
	pm := &PrometheusMonitor{
		baseURL: "http://mist:4242",
	}

	// Minimal data — only CPU
	jsonData := map[string]interface{}{
		"cpu": float64(10),
	}

	trigger := pm.convertNodeAPIToMistTrigger("node-2", jsonData, logging.NewLogger())
	nlu := trigger.GetNodeLifecycleUpdate()

	if nlu.BaseUrl != "http://mist:4242" {
		t.Fatalf("expected fallback to baseURL, got %q", nlu.BaseUrl)
	}
	if nlu.CpuTenths != 100 {
		t.Fatalf("expected 100 cpu tenths, got %d", nlu.CpuTenths)
	}
	if nlu.RamMax != 0 {
		t.Fatalf("expected 0 RAM max, got %d", nlu.RamMax)
	}
	// Default bwlimit when not provided
	if nlu.BwLimit != 128*1024*1024 {
		t.Fatalf("expected default bwlimit, got %d", nlu.BwLimit)
	}
	if nlu.UpSpeed != 0 {
		t.Fatalf("expected 0 up speed on first poll, got %d", nlu.UpSpeed)
	}
}

func TestConvertNodeAPI_NilData(t *testing.T) {
	pm := &PrometheusMonitor{baseURL: "http://mist:4242"}
	trigger := pm.convertNodeAPIToMistTrigger("node-3", nil, logging.NewLogger())
	nlu := trigger.GetNodeLifecycleUpdate()

	if nlu.IsHealthy {
		t.Fatal("expected unhealthy when no mist data")
	}
	if nlu.CpuTenths != 0 {
		t.Fatalf("expected 0 CPU, got %d", nlu.CpuTenths)
	}
}

func TestConvertNodeAPI_LegacyBandwidthFormat(t *testing.T) {
	pm := &PrometheusMonitor{baseURL: "http://mist:4242"}
	jsonData := map[string]interface{}{
		"cpu": float64(10),
		"bandwidth": map[string]interface{}{
			"up":   float64(1000000),
			"down": float64(5000000),
		},
	}

	trigger := pm.convertNodeAPIToMistTrigger("node-4", jsonData, logging.NewLogger())
	nlu := trigger.GetNodeLifecycleUpdate()

	if nlu.UpSpeed != 1000000 {
		t.Fatalf("expected legacy up speed 1000000, got %d", nlu.UpSpeed)
	}
	if nlu.DownSpeed != 5000000 {
		t.Fatalf("expected legacy down speed 5000000, got %d", nlu.DownSpeed)
	}
}

func TestConvertNodeAPI_LegacyRAMFormat(t *testing.T) {
	pm := &PrometheusMonitor{baseURL: "http://mist:4242"}
	jsonData := map[string]interface{}{
		"cpu": float64(10),
		"ram": map[string]interface{}{
			"max":     float64(8000000000),
			"current": float64(4000000000),
		},
	}

	trigger := pm.convertNodeAPIToMistTrigger("node-5", jsonData, logging.NewLogger())
	nlu := trigger.GetNodeLifecycleUpdate()

	if nlu.RamMax != 8000000000 {
		t.Fatalf("expected legacy RAM max, got %d", nlu.RamMax)
	}
	if nlu.RamCurrent != 4000000000 {
		t.Fatalf("expected legacy RAM current, got %d", nlu.RamCurrent)
	}
}

// --- Utility functions ---

func TestEvaluateNodeHealth_Boundary(t *testing.T) {
	if !evaluateNodeHealth(true, 90, 90, 90) {
		t.Fatal("expected healthy at exactly 90%")
	}
	if evaluateNodeHealth(true, 90.1, 90, 90) {
		t.Fatal("expected unhealthy at 90.1% CPU")
	}
}

func TestRolesFromCapabilityFlags(t *testing.T) {
	roles := rolesFromCapabilityFlags("1", "true", "0", "false")
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %d: %v", len(roles), roles)
	}
	if roles[0] != "ingest" || roles[1] != "edge" {
		t.Fatalf("expected [ingest, edge], got %v", roles)
	}

	// Defaults to true when empty
	allRoles := rolesFromCapabilityFlags("", "", "", "")
	if len(allRoles) != 4 {
		t.Fatalf("expected 4 roles for empty flags, got %d: %v", len(allRoles), allRoles)
	}
}

func TestInterpretCapabilityFlag(t *testing.T) {
	tests := []struct {
		value string
		def   bool
		want  bool
	}{
		{"", true, true},
		{"", false, false},
		{"1", false, true},
		{"true", false, true},
		{"yes", false, true},
		{"TRUE", false, true},
		{"0", true, false},
		{"no", true, false},
		{"false", true, false},
	}
	for _, tt := range tests {
		got := interpretCapabilityFlag(tt.value, tt.def)
		if got != tt.want {
			t.Errorf("interpretCapabilityFlag(%q, %v) = %v, want %v", tt.value, tt.def, got, tt.want)
		}
	}
}

// --- Scan functions ---

func setupScanLogger(t *testing.T) {
	t.Helper()
	old := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = old })
}

func TestScanVODDirectory_Empty(t *testing.T) {
	setupScanLogger(t)

	vodDir := filepath.Join(t.TempDir(), "vod")
	if err := os.MkdirAll(vodDir, 0o755); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	size, count := scanVODDirectory(vodDir, idx)

	if size != 0 || count != 0 {
		t.Fatalf("expected 0/0, got size=%d count=%d", size, count)
	}
	if len(idx) != 0 {
		t.Fatalf("expected empty index, got %d entries", len(idx))
	}
}

func TestScanVODDirectory_NonExistent(t *testing.T) {
	setupScanLogger(t)
	idx := make(map[string]*ClipInfo)
	size, count := scanVODDirectory("/nonexistent/vod", idx)
	if size != 0 || count != 0 {
		t.Fatalf("expected 0/0 for non-existent dir, got %d/%d", size, count)
	}
}

func TestScanVODDirectory_WithFiles(t *testing.T) {
	setupScanLogger(t)

	// Need to make fileStabilityThreshold very small for tests
	oldThreshold := fileStabilityThreshold
	fileStabilityThreshold = 0
	t.Cleanup(func() { fileStabilityThreshold = oldThreshold })

	vodDir := filepath.Join(t.TempDir(), "vod")
	if err := os.MkdirAll(vodDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Valid VOD hash (18+ hex chars)
	hash := "aabbccddeeff001122"
	if err := os.WriteFile(filepath.Join(vodDir, hash+".mp4"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	// Invalid (short hash) — should be skipped
	if err := os.WriteFile(filepath.Join(vodDir, "short.mp4"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	// Directory — should be skipped
	if err := os.MkdirAll(filepath.Join(vodDir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No extension — should be skipped
	if err := os.WriteFile(filepath.Join(vodDir, "aabbccddeeff001133"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	size, count := scanVODDirectory(vodDir, idx)

	if count != 1 {
		t.Fatalf("expected 1 artifact, got %d", count)
	}
	if size != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", size)
	}
	info, ok := idx[hash]
	if !ok {
		t.Fatal("expected hash in index")
	}
	if info.Format != "mp4" {
		t.Fatalf("expected format 'mp4', got %q", info.Format)
	}
	if info.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_VOD {
		t.Fatalf("expected VOD artifact type, got %v", info.ArtifactType)
	}
}

func TestScanClipsDirectory_Empty(t *testing.T) {
	setupScanLogger(t)
	clipsDir := filepath.Join(t.TempDir(), "clips")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	size, count := scanClipsDirectory(clipsDir, idx)
	if size != 0 || count != 0 {
		t.Fatalf("expected 0/0, got %d/%d", size, count)
	}
}

func TestScanClipsDirectory_NonExistent(t *testing.T) {
	setupScanLogger(t)
	idx := make(map[string]*ClipInfo)
	size, count := scanClipsDirectory("/nonexistent/clips", idx)
	if size != 0 || count != 0 {
		t.Fatalf("expected 0/0 for non-existent dir, got %d/%d", size, count)
	}
}

func TestScanClipsDirectory_NestedStreamDirs(t *testing.T) {
	setupScanLogger(t)

	oldThreshold := fileStabilityThreshold
	fileStabilityThreshold = 0
	t.Cleanup(func() { fileStabilityThreshold = oldThreshold })

	base := t.TempDir()
	clipsDir := filepath.Join(base, "clips")
	streamDir := filepath.Join(clipsDir, "my-stream")
	if err := os.MkdirAll(streamDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "aabbccddeeff001122"
	content := make([]byte, 2048)
	if err := os.WriteFile(filepath.Join(streamDir, hash+".mp4"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	size, count := scanClipsDirectory(clipsDir, idx)

	if count != 1 {
		t.Fatalf("expected 1 clip, got %d", count)
	}
	if size != 2048 {
		t.Fatalf("expected 2048 bytes, got %d", size)
	}
	info := idx[hash]
	if info == nil {
		t.Fatal("expected hash in index")
	}
	if info.StreamName != "my-stream" {
		t.Fatalf("expected stream name 'my-stream', got %q", info.StreamName)
	}
	if info.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_CLIP {
		t.Fatalf("expected CLIP type, got %v", info.ArtifactType)
	}
}

func TestScanClipsDirectory_WithDtsh(t *testing.T) {
	setupScanLogger(t)

	oldThreshold := fileStabilityThreshold
	fileStabilityThreshold = 0
	t.Cleanup(func() { fileStabilityThreshold = oldThreshold })

	base := t.TempDir()
	clipsDir := filepath.Join(base, "clips")
	streamDir := filepath.Join(clipsDir, "stream-a")
	if err := os.MkdirAll(streamDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "aabbccddeeff001122"
	if err := os.WriteFile(filepath.Join(streamDir, hash+".mp4"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(streamDir, hash+".mp4.dtsh"), make([]byte, 256), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	scanClipsDirectory(clipsDir, idx)

	info := idx[hash]
	if info == nil {
		t.Fatal("expected hash in index")
	}
	if !info.HasDtsh {
		t.Fatal("expected HasDtsh=true")
	}
}

func TestScanDVRDirectory_NestedStructure(t *testing.T) {
	setupScanLogger(t)

	oldThreshold := fileStabilityThreshold
	fileStabilityThreshold = 0
	t.Cleanup(func() { fileStabilityThreshold = oldThreshold })

	base := t.TempDir()
	dvrDir := filepath.Join(base, "dvr")
	dvrHash := "aabbccddeeff001122"
	recordingDir := filepath.Join(dvrDir, "stream-1", dvrHash)
	segmentsDir := filepath.Join(recordingDir, "segments")
	if err := os.MkdirAll(segmentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create manifest
	manifest := "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:6.0,\nsegments/chunk000.ts\n#EXTINF:6.0,\nsegments/chunk001.ts\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(recordingDir, dvrHash+".m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create segment files
	if err := os.WriteFile(filepath.Join(segmentsDir, "chunk000.ts"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segmentsDir, "chunk001.ts"), make([]byte, 3000), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	size, count := scanDVRDirectory(dvrDir, idx)

	if count != 1 {
		t.Fatalf("expected 1 DVR artifact, got %d", count)
	}
	// manifest size + 5000 + 3000
	manifestSize := int64(len(manifest))
	expectedSize := uint64(manifestSize) + 5000 + 3000
	if size != expectedSize {
		t.Fatalf("expected %d total bytes, got %d", expectedSize, size)
	}
	info := idx[dvrHash]
	if info == nil {
		t.Fatal("expected DVR hash in index")
	}
	if info.Format != "m3u8" {
		t.Fatalf("expected format 'm3u8', got %q", info.Format)
	}
	if info.SegmentCount != 2 {
		t.Fatalf("expected 2 segments, got %d", info.SegmentCount)
	}
	if info.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_DVR {
		t.Fatalf("expected DVR type, got %v", info.ArtifactType)
	}
}

func TestScanDVRDirectory_NonExistent(t *testing.T) {
	setupScanLogger(t)
	idx := make(map[string]*ClipInfo)
	size, count := scanDVRDirectory("/nonexistent/dvr", idx)
	if size != 0 || count != 0 {
		t.Fatalf("expected 0/0, got %d/%d", size, count)
	}
}

func TestScanDVRDirectory_WithDtsh(t *testing.T) {
	setupScanLogger(t)

	oldThreshold := fileStabilityThreshold
	fileStabilityThreshold = 0
	t.Cleanup(func() { fileStabilityThreshold = oldThreshold })

	base := t.TempDir()
	dvrDir := filepath.Join(base, "dvr")
	dvrHash := "aabbccddeeff001122"
	recordingDir := filepath.Join(dvrDir, "stream-1", dvrHash)
	if err := os.MkdirAll(recordingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := "#EXTM3U\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(recordingDir, dvrHash+".m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordingDir, dvrHash+".dtsh"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := make(map[string]*ClipInfo)
	scanDVRDirectory(dvrDir, idx)

	if !idx[dvrHash].HasDtsh {
		t.Fatal("expected HasDtsh=true for DVR with .dtsh file")
	}
}

func TestIsHex(t *testing.T) {
	if !isHex("aabbccdd") {
		t.Fatal("expected valid hex")
	}
	if isHex("") {
		t.Fatal("expected empty string to be invalid hex")
	}
	if isHex("xyz123") {
		t.Fatal("expected non-hex chars to fail")
	}
	// Odd-length hex should fail (hex.DecodeString requires even length)
	if isHex("abc") {
		t.Fatal("expected odd-length hex to fail")
	}
}

func TestCalculateDVRSegmentSize(t *testing.T) {
	oldThreshold := fileStabilityThreshold
	fileStabilityThreshold = 0
	t.Cleanup(func() { fileStabilityThreshold = oldThreshold })

	base := t.TempDir()
	segDir := filepath.Join(base, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := "#EXTM3U\n#EXTINF:6.0,\nsegments/a.ts\n#EXTINF:6.0,\nsegments/b.ts\n"
	manifestPath := filepath.Join(base, "manifest.m3u8")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "a.ts"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "b.ts"), make([]byte, 2000), 0o644); err != nil {
		t.Fatal(err)
	}

	size, count := calculateDVRSegmentSize(manifestPath, base)
	if count != 2 {
		t.Fatalf("expected 2 segments, got %d", count)
	}
	if size != 3000 {
		t.Fatalf("expected 3000 bytes, got %d", size)
	}
}

func TestCalculateDVRSegmentSize_MissingManifest(t *testing.T) {
	size, count := calculateDVRSegmentSize("/nonexistent/manifest.m3u8", "/nonexistent")
	if size != 0 || count != 0 {
		t.Fatalf("expected 0/0, got %d/%d", size, count)
	}
}

// --- GetStoredArtifacts ---

func TestGetStoredArtifacts_NilMonitor(t *testing.T) {
	old := prometheusMonitor
	prometheusMonitor = nil
	t.Cleanup(func() { prometheusMonitor = old })

	artifacts := GetStoredArtifacts()
	if artifacts != nil {
		t.Fatalf("expected nil for nil monitor, got %v", artifacts)
	}
}

func TestGetStoredArtifacts_WithIndex(t *testing.T) {
	old := prometheusMonitor
	t.Cleanup(func() { prometheusMonitor = old })

	now := time.Now()
	prometheusMonitor = &PrometheusMonitor{
		artifactIndex: map[string]*ClipInfo{
			"hash-a": {
				FilePath:     "/data/clips/hash-a.mp4",
				StreamName:   "stream-1",
				Format:       "mp4",
				SizeBytes:    4096,
				CreatedAt:    now,
				HasDtsh:      true,
				AccessCount:  5,
				LastAccessed: now,
				ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
			},
		},
	}

	artifacts := GetStoredArtifacts()
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	a := artifacts[0]
	if a.ClipHash != "hash-a" {
		t.Fatalf("expected hash-a, got %q", a.ClipHash)
	}
	if a.Format != "mp4" {
		t.Fatalf("expected mp4, got %q", a.Format)
	}
	if !a.HasDtsh {
		t.Fatal("expected HasDtsh=true")
	}
	if a.AccessCount != 5 {
		t.Fatalf("expected access count 5, got %d", a.AccessCount)
	}
}

// --- enrichNodeLifecycleTrigger ---

func TestEnrichNodeLifecycleTrigger_Capabilities(t *testing.T) {
	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
			NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{
				NodeId: "node-1",
			},
		},
	}

	enrichNodeLifecycleTrigger(trigger, "1", "true", "0", "false", []string{"ingest", "edge"})

	nlu := trigger.GetNodeLifecycleUpdate()
	caps := nlu.Capabilities
	if caps == nil {
		t.Fatal("expected capabilities to be set")
	}
	if !caps.Ingest {
		t.Fatal("expected ingest=true")
	}
	if !caps.Edge {
		t.Fatal("expected edge=true")
	}
	if caps.Storage {
		t.Fatal("expected storage=false")
	}
	if caps.Processing {
		t.Fatal("expected processing=false")
	}
	if len(caps.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(caps.Roles))
	}
}

func TestEnrichNodeLifecycleTrigger_NilPayload(t *testing.T) {
	trigger := &pb.MistTrigger{}
	// Should not panic
	enrichNodeLifecycleTrigger(trigger, "1", "1", "1", "1", nil)
}

// --- safeInt32 (if exported or reachable) & JSON round-trip helpers ---

func TestStreamAPIToMistTrigger_TrackDetailsJSON(t *testing.T) {
	tracks := []map[string]interface{}{
		{"type": "video", "codec": "H264", "height": 1080},
	}

	trigger := convertStreamAPIToMistTrigger("n", "s", "s", nil, nil, tracks, 1, logging.NewLogger())
	slu := trigger.GetStreamLifecycleUpdate()

	if slu.GetTrackDetailsJson() == "" {
		t.Fatal("expected track details JSON")
	}
	var decoded []map[string]interface{}
	if err := json.Unmarshal([]byte(slu.GetTrackDetailsJson()), &decoded); err != nil {
		t.Fatalf("failed to decode track details JSON: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 track in JSON, got %d", len(decoded))
	}
}
