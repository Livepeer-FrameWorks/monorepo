package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
)

// Manager maintains desired vs current MistServer configuration and reconciles them via the Mist API.
type Manager struct {
	mu             sync.Mutex
	mistClient     *mist.Client
	logger         logging.Logger
	lastSeed       *pb.ConfigSeed
	lastAppliedSum string
}

var manager *Manager

// InitManager initializes the singleton config manager with logger and Mist client.
func InitManager(logger logging.Logger) {
	if manager != nil {
		return
	}
	manager = &Manager{
		mistClient: mist.NewClient(logger),
		logger:     logger,
	}
}

// ApplySeed stores the latest ConfigSeed and triggers reconcile.
func ApplySeed(seed *pb.ConfigSeed) {
	if manager == nil || seed == nil {
		return
	}
	manager.mu.Lock()
	manager.lastSeed = seed
	manager.mu.Unlock()
	go manager.reconcile()
}

// GetTenantID returns the tenant_id from the last applied ConfigSeed
func GetTenantID() string {
	if manager == nil {
		return ""
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.lastSeed == nil {
		return ""
	}
	return manager.lastSeed.GetTenantId()
}

// GetProcessingConfig returns the processing configuration from the last applied ConfigSeed
func GetProcessingConfig() *pb.ProcessingConfig {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.lastSeed == nil {
		return nil
	}
	return manager.lastSeed.GetProcessing()
}

// GetOperationalMode returns the authoritative operational mode from the last applied ConfigSeed.
// Foghorn is the authority; this is what Helmsman should report in heartbeats.
func GetOperationalMode() pb.NodeOperationalMode {
	if manager == nil {
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.lastSeed == nil {
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
	mode := manager.lastSeed.GetOperationalMode()
	if mode == pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
	return mode
}

// IsLivepeerGatewayAvailable returns true if Livepeer Gateway is configured and available
func IsLivepeerGatewayAvailable() bool {
	cfg := GetProcessingConfig()
	return cfg != nil && cfg.GetLivepeerGatewayAvailable()
}

// GetLivepeerGatewayURL returns the Livepeer Gateway URL if available
func GetLivepeerGatewayURL() string {
	cfg := GetProcessingConfig()
	if cfg == nil {
		return ""
	}
	return cfg.GetLivepeerGatewayUrl()
}

// reconcile computes desired config from seed + env and applies minimal changes idempotently.
func (m *Manager) reconcile() {
	m.mu.Lock()
	seed := m.lastSeed
	m.mu.Unlock()
	if seed == nil {
		return
	}

	// Log processing config if present
	if proc := seed.GetProcessing(); proc != nil {
		if proc.GetLivepeerGatewayAvailable() {
			m.logger.WithField("gateway_url", proc.GetLivepeerGatewayUrl()).Info("Livepeer Gateway available for H.264 transcoding")
		} else {
			m.logger.Debug("No Livepeer Gateway configured, using local processing only")
		}
	}

	current, _ := m.mistClient.ConfigBackup()
	desiredConfig := map[string]interface{}{}

	// Location (from seed)
	if seed.GetLatitude() != 0 || seed.GetLongitude() != 0 || seed.GetLocationName() != "" {
		desiredConfig["location"] = map[string]interface{}{
			"lat":  seed.GetLatitude(),
			"lon":  seed.GetLongitude(),
			"name": seed.GetLocationName(),
		}
	}

	// Prometheus passphrase (from env)
	if prom := os.Getenv("MIST_PASSWORD"); prom != "" {
		desiredConfig["prometheus"] = prom
	}

	// Triggers (pointed at Helmsman webhooks)
	webhookBase := os.Getenv("HELMSMAN_WEBHOOK_URL")
	if webhookBase == "" {
		webhookBase = "http://localhost:18007"
	}
	triggers := map[string]interface{}{
		"PUSH_REWRITE": []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/push_rewrite"), "sync": true}},
		// "PLAY_REWRITE":      []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/play_rewrite"), "sync": true, "streams": []string{"vod+", "live+"}}},
		"PLAY_REWRITE":      []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/play_rewrite"), "sync": true}},
		"STREAM_SOURCE":     []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_source"), "sync": true}},
		"PUSH_OUT_START":    []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/push_out_start"), "sync": true}},
		"PUSH_END":          []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/push_end"), "sync": false}},
		"USER_NEW":          []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/user_new"), "sync": true, "default": "true"}},
		"USER_END":          []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/user_end"), "sync": false}},
		"STREAM_BUFFER":     []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_buffer"), "sync": false}},
		"STREAM_END":        []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_end"), "sync": false}},
		"LIVE_TRACK_LIST":   []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/live_track_list"), "sync": false}},
		"RECORDING_END":     []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/recording_end"), "sync": false}},
		"RECORDING_SEGMENT": []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/recording_segment"), "sync": false}},
		// Processing billing triggers (for tracking transcoding usage)
		"LIVEPEER_SEGMENT_COMPLETE":           []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/livepeer_segment_complete"), "sync": false}},
		"PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE": []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/process_av_segment_complete"), "sync": false}},
	}
	desiredConfig["triggers"] = triggers

	_ = m.ensureProtocols(current)
	_ = m.ensureStreams(seed)

	if len(desiredConfig) > 0 {
		if _, err := m.mistClient.UpdateConfig(desiredConfig); err != nil {
			m.logger.WithError(err).Warn("UpdateConfig failed")
		}
	}

	_ = m.mistClient.Save()

	// Record applied signature
	if sum := hashSeed(seed); sum != "" {
		m.mu.Lock()
		m.lastAppliedSum = sum
		m.mu.Unlock()
	}
}

func (m *Manager) ensureProtocols(current map[string]interface{}) error {
	// Get EDGE_PUBLIC_URL (full URL like http://localhost:18090/view)
	edgeURL := os.Getenv("EDGE_PUBLIC_URL")

	// pubaddr is used for HTTP URLs (full public URL, typically including /view/).
	// pubhost is used by MistServer WebRTC for ICE candidates (hostname only; no scheme/path).
	httpPubURL := ""
	webrtcPubHost := ""
	if edgeURL == "" {
		m.logger.Warn("EDGE_PUBLIC_URL not set, skipping protocol pubaddr/pubhost configuration")
	} else {
		httpPubURL = strings.TrimRight(edgeURL, "/") + "/"
		webrtcPubHost = hostnameFromPublicURL(edgeURL)
		if webrtcPubHost == "" {
			m.logger.WithFields(logging.Fields{"edge_public_url": edgeURL}).Warn("Could not derive WebRTC pubhost from EDGE_PUBLIC_URL; leaving pubhost unchanged")
		}
	}

	// Build a map of currently configured connectors with their settings
	existingProtos := map[string]map[string]interface{}{}
	if cfg, ok := current["config"].(map[string]interface{}); ok {
		if protos, ok := cfg["protocols"].([]interface{}); ok {
			for _, p := range protos {
				if pm, ok := p.(map[string]interface{}); ok {
					if name, ok := pm["connector"].(string); ok && name != "" {
						existingProtos[name] = pm
					}
				}
			}
		}
	}

	need := []map[string]interface{}{}
	needsUpdate := false

	// HTTP protocol - check if exists and has correct pubaddr
	if existing, ok := existingProtos["HTTP"]; !ok {
		entry := map[string]interface{}{"connector": "HTTP"}
		if httpPubURL != "" {
			entry["pubaddr"] = []string{httpPubURL}
		}
		need = append(need, entry)
	} else if httpPubURL != "" {
		// Check if pubaddr needs updating
		currentPubaddr := ""
		if pa, ok := existing["pubaddr"].([]interface{}); ok && len(pa) > 0 {
			if s, ok := pa[0].(string); ok {
				currentPubaddr = s
			}
		}
		if currentPubaddr != httpPubURL {
			m.logger.WithFields(logging.Fields{
				"current": currentPubaddr,
				"desired": httpPubURL,
			}).Info("HTTP pubaddr needs update")
			needsUpdate = true
		}
	}

	// WebRTC protocol - check if exists and has correct pubhost
	if existing, ok := existingProtos["WebRTC"]; !ok {
		entry := map[string]interface{}{"connector": "WebRTC", "bindhost": "0.0.0.0"}
		if webrtcPubHost != "" {
			entry["pubhost"] = webrtcPubHost
		}
		need = append(need, entry)
	} else if webrtcPubHost != "" {
		// Check if pubhost needs updating
		currentPubhost := ""
		if ph, ok := existing["pubhost"].(string); ok {
			currentPubhost = ph
		}
		if currentPubhost != webrtcPubHost {
			m.logger.WithFields(logging.Fields{
				"current": currentPubhost,
				"desired": webrtcPubHost,
			}).Info("WebRTC pubhost needs update")
			needsUpdate = true
		}
	}

	// DTSC - ensure exists for inter-node communication
	if _, ok := existingProtos["DTSC"]; !ok {
		entry := map[string]interface{}{"connector": "DTSC"}
		need = append(need, entry)
	}

	// WSRaw - ensure exists for WebCodecs playback
	if _, ok := existingProtos["WSRaw"]; !ok {
		entry := map[string]interface{}{"connector": "WSRaw"}
		need = append(need, entry)
	}

	// Add missing protocols
	if len(need) > 0 {
		if err := m.mistClient.AddProtocols(need); err != nil {
			return err
		}
	}

	// Update existing protocols if pubaddr/pubhost is wrong
	if needsUpdate && (httpPubURL != "" || webrtcPubHost != "") {
		protocols := []map[string]interface{}{}
		if httpPubURL != "" {
			protocols = append(protocols, map[string]interface{}{"connector": "HTTP", "pubaddr": []string{httpPubURL}})
		}
		if webrtcPubHost != "" {
			protocols = append(protocols, map[string]interface{}{"connector": "WebRTC", "bindhost": "0.0.0.0", "pubhost": webrtcPubHost})
		}

		updateConfig := map[string]interface{}{"protocols": protocols}
		if _, err := m.mistClient.UpdateConfig(updateConfig); err != nil {
			m.logger.WithError(err).Warn("Failed to update protocol pubaddr/pubhost")
		} else {
			m.logger.Info("Updated HTTP pubaddr and WebRTC pubhost")
		}
	}

	return nil
}

func hostnameFromPublicURL(edgePublicURL string) string {
	raw := strings.TrimSpace(edgePublicURL)
	if raw == "" {
		return ""
	}

	// Accept forms like:
	// - http://localhost:18090/view
	// - https://edge.example.com/view
	// - //localhost:18090/view
	// - localhost:18090/view
	if strings.HasPrefix(raw, "//") {
		raw = "http:" + raw
	} else if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return u.Hostname()
}

func (m *Manager) ensureStreams(seed *pb.ConfigSeed) error {
	if seed == nil || len(seed.GetTemplates()) == 0 {
		return nil
	}
	base := os.Getenv("FOGHORN_URL")
	if base == "" {
		base = "http://foghorn:18008"
	}
	source := "balance:" + base + "?fallback=push://"

	// Build default processes based on ProcessingConfig
	defaultProcs := m.buildDefaultProcesses(seed.GetProcessing())

	streams := map[string]map[string]interface{}{}
	for _, t := range seed.GetTemplates() {
		def := t.GetDef()
		if def == nil || def.GetName() == "" {
			continue
		}
		entry := map[string]interface{}{
			"name":          def.GetName(),
			"source":        source,
			"realtime":      def.GetRealtime(),
			"stop_sessions": def.GetStopSessions(),
			"tags":          def.GetTags(),
		}

		// Start with default processes (audio transcode, Livepeer if available)
		processes := make([]map[string]interface{}, len(defaultProcs))
		copy(processes, defaultProcs)

		// Append any template-defined processes
		if procs := def.GetProcesses(); len(procs) > 0 {
			for _, p := range procs {
				pm := map[string]interface{}{"process": p.GetProcess()}
				if c := strings.TrimSpace(p.GetCodec()); c != "" {
					pm["codec"] = c
				}
				if b := p.GetBitrate(); b > 0 {
					pm["bitrate"] = b
				}
				if s := strings.TrimSpace(p.GetTrackSelect()); s != "" {
					pm["track_select"] = s
				}
				if s := strings.TrimSpace(p.GetTrackInhibit()); s != "" {
					pm["track_inhibit"] = s
				}
				if r := strings.TrimSpace(p.GetRestartType()); r != "" {
					pm["restart_type"] = r
				}
				if p.GetInconsequential() {
					pm["inconsequential"] = true
				}
				if p.GetExitUnmask() {
					pm["exit_unmask"] = true
				}
				processes = append(processes, pm)
			}
		}

		if len(processes) > 0 {
			entry["processes"] = processes
		}
		streams[def.GetName()] = entry
	}
	if len(streams) == 0 {
		return nil
	}
	return m.mistClient.AddStreams(streams)
}

// buildDefaultProcesses returns MistServer processes that should always be enabled:
// - Audio transcode (AAC↔Opus) for WebRTC-HLS compatibility
// - Livepeer ABR transcoding when Gateway is available
func (m *Manager) buildDefaultProcesses(proc *pb.ProcessingConfig) []map[string]interface{} {
	var procs []map[string]interface{}

	// Always add audio transcode processes for WebRTC-HLS compatibility
	// AAC → Opus (for WebRTC viewers)
	procs = append(procs, map[string]interface{}{
		"process":       "AV",
		"codec":         "opus",
		"track_inhibit": "audio=opus",
		"track_select":  "video=none",
		"x-LSP-name":    "Audio to Opus",
	})
	// Opus → AAC (for HLS/native viewers)
	procs = append(procs, map[string]interface{}{
		"process":       "AV",
		"codec":         "AAC",
		"track_inhibit": "audio=aac",
		"track_select":  "video=none",
		"x-LSP-name":    "Audio to AAC",
	})

	// Add Livepeer process if Gateway is available
	if proc != nil && proc.GetLivepeerGatewayAvailable() {
		gatewayURL := proc.GetLivepeerGatewayUrl()
		// Format for MistProcLivepeer: hardcoded_broadcasters JSON array
		broadcasters := fmt.Sprintf(`[{"address":"%s"}]`, gatewayURL)

		procs = append(procs, map[string]interface{}{
			"process":                "Livepeer",
			"hardcoded_broadcasters": broadcasters,
			"target_profiles": []map[string]interface{}{
				{
					"name":          "480p",
					"bitrate":       512000,
					"fps":           15,
					"height":        480,
					"profile":       "H264ConstrainedHigh",
					"track_inhibit": "video=<850x480",
				},
				{
					"name":          "720p",
					"bitrate":       1024000,
					"fps":           25,
					"height":        720,
					"profile":       "H264ConstrainedHigh",
					"track_inhibit": "video=<1281x720",
				},
			},
			"track_inhibit": "video=<850x480", // Only run for streams >= 850x480
			"x-LSP-name":    "ABR Transcode",
		})

		m.logger.WithFields(logging.Fields{
			"gateway_url":    gatewayURL,
			"profiles":       2,
			"min_resolution": "850x480",
		}).Info("Livepeer ABR transcoding enabled for streams")
	}

	return procs
}

func join(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func hashSeed(seed *pb.ConfigSeed) string {
	if seed == nil {
		return ""
	}
	// Create a stable representation of seed fields we care about
	flat := struct {
		Node   string   `json:"n"`
		Lat    float64  `json:"a"`
		Lon    float64  `json:"o"`
		Loc    string   `json:"l"`
		TplIDs []string `json:"t"`
	}{
		Node: seed.GetNodeId(),
		Lat:  seed.GetLatitude(),
		Lon:  seed.GetLongitude(),
		Loc:  seed.GetLocationName(),
	}
	ids := make([]string, 0, len(seed.GetTemplates()))
	for _, t := range seed.GetTemplates() {
		ids = append(ids, t.GetId())
	}
	sort.Strings(ids)
	flat.TplIDs = ids
	b, _ := json.Marshal(flat)
	sum := sha256.Sum256(b)
	return string(sum[:])
}
