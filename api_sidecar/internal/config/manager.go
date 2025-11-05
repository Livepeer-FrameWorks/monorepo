package config

import (
	"crypto/sha256"
	"encoding/json"
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

// reconcile computes desired config from seed + env and applies minimal changes idempotently.
func (m *Manager) reconcile() {
	m.mu.Lock()
	seed := m.lastSeed
	m.mu.Unlock()
	if seed == nil {
		return
	}

	// Step 1: Read current config backup (best-effort)
	current, _ := m.mistClient.ConfigBackup()

	// Step 2: Build partial desired config for UpdateConfig (location + triggers)
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
		"PUSH_REWRITE":    []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/push_rewrite"), "sync": true}},
		"DEFAULT_STREAM":  []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/default_stream"), "sync": true}},
		"STREAM_SOURCE":   []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_source"), "sync": true}},
		"PUSH_OUT_START":  []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/push_out_start"), "sync": true}},
		"PUSH_END":        []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/push_end"), "sync": false}},
		"USER_NEW":        []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/user_new"), "sync": true, "default": "true"}},
		"USER_END":        []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/user_end"), "sync": false}},
		"STREAM_BUFFER":   []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_buffer"), "sync": false}},
		"STREAM_END":      []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_end"), "sync": false}},
		"LIVE_TRACK_LIST": []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/live_track_list"), "sync": false}},
		"LIVE_BANDWIDTH":  []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/live_bandwidth"), "sync": false}},
		"RECORDING_END":   []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/recording_end"), "sync": false}},
	}
	desiredConfig["triggers"] = triggers

	// Step 3: Ensure essential protocols exist (HTTP, WebRTC)
	_ = m.ensureProtocols(current)

	// Step 4: Ensure streams from seed templates exist/updated
	_ = m.ensureStreams(seed)

	// Step 5: Apply partial config
	if len(desiredConfig) > 0 {
		if _, err := m.mistClient.UpdateConfig(desiredConfig); err != nil {
			m.logger.WithError(err).Warn("UpdateConfig failed")
		}
	}

	// Step 6: Save to persist
	_ = m.mistClient.Save()

	// Record applied signature
	if sum := hashSeed(seed); sum != "" {
		m.mu.Lock()
		m.lastAppliedSum = sum
		m.mu.Unlock()
	}
}

func (m *Manager) ensureProtocols(current map[string]interface{}) error {
	// Build a set of currently configured connectors (best-effort)
	existing := map[string]struct{}{}
	if cfg, ok := current["config"].(map[string]interface{}); ok {
		if protos, ok := cfg["protocols"].([]interface{}); ok {
			for _, p := range protos {
				if m, ok := p.(map[string]interface{}); ok {
					if name, ok := m["connector"].(string); ok && name != "" {
						existing[name] = struct{}{}
					}
				}
			}
		}
	}

	need := []map[string]interface{}{}
	if _, ok := existing["HTTP"]; !ok {
		entry := map[string]interface{}{"connector": "HTTP"}
		// Optional: pubaddr uses EDGE_PUBLIC_HOSTNAME
		if host := os.Getenv("EDGE_PUBLIC_HOSTNAME"); host != "" {
			entry["pubaddr"] = []string{"http://" + host + ":8080/view/"}
		}
		need = append(need, entry)
	}
	if _, ok := existing["WebRTC"]; !ok {
		entry := map[string]interface{}{"connector": "WebRTC", "bindhost": "0.0.0.0"}
		if host := os.Getenv("EDGE_PUBLIC_HOSTNAME"); host != "" {
			entry["pubhost"] = host + ":8080/view"
		}
		need = append(need, entry)
	}
	if len(need) == 0 {
		return nil
	}
	return m.mistClient.AddProtocols(need)
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
		// Map processes
		if procs := def.GetProcesses(); len(procs) > 0 {
			arr := make([]map[string]interface{}, 0, len(procs))
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
				arr = append(arr, pm)
			}
			entry["processes"] = arr
		}
		streams[def.GetName()] = entry
	}
	if len(streams) == 0 {
		return nil
	}
	return m.mistClient.AddStreams(streams)
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
