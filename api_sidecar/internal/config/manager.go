package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	lastCaddyHash  string
	caddyActivated bool
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

// reconcile computes desired config from seed + env and applies minimal changes idempotently.
func (m *Manager) reconcile() {
	m.mu.Lock()
	seed := m.lastSeed
	m.mu.Unlock()
	if seed == nil {
		return
	}

	if len(seed.GetCaBundle()) > 0 {
		m.applyCABundle(seed.GetCaBundle())
	}
	m.applyTelemetryConfig(seed.GetTelemetry())

	certChanged := false
	if tls := seed.GetTls(); tls != nil {
		certChanged = m.applyTLSBundle(tls)
	} else {
		m.removeTLSBundle()
	}

	if site := seed.GetSite(); site != nil {
		m.activateCaddy(seed, certChanged)
	} else if certChanged {
		m.reloadCaddy(nil)
	}
	current, err := m.mistClient.ConfigBackup()
	if err != nil {
		m.logger.WithError(err).Warn("ConfigBackup failed, skipping reconcile")
		return
	}
	desiredConfig := map[string]interface{}{}

	// Location (from seed)
	if seed.GetLatitude() != 0 || seed.GetLongitude() != 0 || seed.GetLocationName() != "" {
		desiredConfig["location"] = map[string]interface{}{
			"lat":  math.Round(seed.GetLatitude()*1e4) / 1e4,
			"lon":  math.Round(seed.GetLongitude()*1e4) / 1e4,
			"name": seed.GetLocationName(),
		}
	}

	desiredConfig["prometheus"] = mist.MetricsConfigValue

	// Trusted proxies: localhost (IPv4 + IPv6) + "nginx" Docker service name
	desiredConfig["trustedproxy"] = []string{"127.0.0.1", "::1", "localhost", "nginx"}

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
		"THUMBNAIL_UPDATED":                   []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/thumbnail_updated"), "sync": false}},
		"STREAM_PROCESS":                      []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/stream_process"), "sync": true, "streams": []string{"live+", "processing+", "vod+"}}},
		"PROCESS_EXIT":                        []interface{}{map[string]interface{}{"handler": join(webhookBase, "/webhooks/mist/process_exit"), "sync": false, "streams": []string{"processing+"}}},
	}
	desiredConfig["triggers"] = triggers

	if err := m.ensureProtocols(current); err != nil {
		m.logger.WithError(err).Warn("ensureProtocols failed")
	}
	if err := m.ensureStreams(seed); err != nil {
		m.logger.WithError(err).Warn("ensureStreams failed")
	}

	if len(desiredConfig) > 0 {
		if _, err := m.mistClient.UpdateConfig(desiredConfig); err != nil {
			m.logger.WithError(err).Warn("UpdateConfig failed")
		}
	}

	if err := m.mistClient.Save(); err != nil {
		m.logger.WithError(err).Warn("Mist config save failed")
	}

	// Record applied signature
	if sum := hashSeed(seed); sum != "" {
		m.mu.Lock()
		m.lastAppliedSum = sum
		m.mu.Unlock()
	}
}

func grpcCABundlePath() string {
	if path := strings.TrimSpace(os.Getenv("GRPC_TLS_CA_PATH")); path != "" {
		return path
	}
	return "/etc/frameworks/pki/ca.crt"
}

func edgeTelemetryTokenPath() string {
	return "/etc/frameworks/telemetry/token"
}

func (m *Manager) applyTelemetryConfig(cfg *pb.EdgeTelemetryConfig) bool {
	tokenPath := edgeTelemetryTokenPath()
	if cfg == nil || !cfg.GetEnabled() || strings.TrimSpace(cfg.GetBearerToken()) == "" {
		if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
			m.logger.WithError(err).WithField("path", tokenPath).Warn("Failed to remove edge telemetry token")
			return false
		}
		return false
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		m.logger.WithError(err).Warn("Failed to create edge telemetry token directory")
		return false
	}
	if err := atomicWriteFile(tokenPath, []byte(cfg.GetBearerToken()+"\n"), 0o600); err != nil {
		m.logger.WithError(err).WithField("path", tokenPath).Warn("Failed to write edge telemetry token")
		return false
	}
	m.logger.WithFields(logging.Fields{
		"path":       tokenPath,
		"expires_at": cfg.GetExpiresAt(),
	}).Info("Applied edge telemetry token from ConfigSeed")
	return true
}

func (m *Manager) applyCABundle(bundle []byte) bool {
	caPath := grpcCABundlePath()
	if len(bundle) == 0 {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(caPath), 0o755); err != nil {
		m.logger.WithError(err).Warn("Failed to create gRPC CA bundle directory")
		return false
	}
	if existing, err := os.ReadFile(caPath); err == nil && bytes.Equal(existing, bundle) {
		return false
	}
	if err := os.WriteFile(caPath, bundle, 0o600); err != nil {
		m.logger.WithError(err).Warn("Failed to write gRPC CA bundle")
		return false
	}
	m.logger.WithField("path", caPath).Info("Applied gRPC CA bundle from ConfigSeed")
	return true
}

// applyTLSBundle writes cert/key files to disk. Returns true if files were changed.
func (m *Manager) applyTLSBundle(bundle *pb.TLSCertBundle) bool {
	certPath := "/etc/frameworks/certs/cert.pem"
	keyPath := "/etc/frameworks/certs/key.pem"

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		m.logger.WithError(err).Warn("Failed to create TLS certificate directory")
		return false
	}

	certBytes := []byte(bundle.GetCertPem())
	keyBytes := []byte(bundle.GetKeyPem())
	if len(certBytes) == 0 || len(keyBytes) == 0 {
		m.logger.Warn("Received empty TLS bundle in ConfigSeed")
		return false
	}

	if existing, err := os.ReadFile(certPath); err == nil && bytes.Equal(existing, certBytes) {
		if existingKey, keyErr := os.ReadFile(keyPath); keyErr == nil && bytes.Equal(existingKey, keyBytes) {
			return false
		}
	}

	if err := os.WriteFile(certPath, certBytes, 0o600); err != nil {
		m.logger.WithError(err).Warn("Failed to write TLS certificate file")
		return false
	}
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		m.logger.WithError(err).Warn("Failed to write TLS key file")
		return false
	}

	m.logger.WithFields(logging.Fields{
		"domain":     bundle.GetDomain(),
		"expires_at": bundle.GetExpiresAt(),
	}).Info("Applied TLS certificate bundle from ConfigSeed")

	return true
}

func (m *Manager) removeTLSBundle() {
	certPath := "/etc/frameworks/certs/cert.pem"
	keyPath := "/etc/frameworks/certs/key.pem"

	certGone := true
	if _, err := os.Stat(certPath); err == nil {
		if err := os.Remove(certPath); err != nil {
			m.logger.WithError(err).Warn("Failed to remove TLS certificate file")
			certGone = false
		}
	}
	if _, err := os.Stat(keyPath); err == nil {
		if err := os.Remove(keyPath); err != nil {
			m.logger.WithError(err).Warn("Failed to remove TLS key file")
			certGone = false
		}
	}

	if certGone {
		m.logger.Info("Removed TLS certificate files")
		m.reloadCaddy(nil)
	}
}

// activateCaddy renders the production Caddyfile from ConfigSeed and pushes it to Caddy.
func (m *Manager) activateCaddy(seed *pb.ConfigSeed, certChanged bool) {
	site := seed.GetSite()
	if site == nil || site.GetSiteAddress() == "" {
		if certChanged {
			m.reloadCaddy(nil)
		}
		return
	}

	params := CaddyfileParams{
		SiteAddress:      site.GetSiteAddress(),
		AcmeEmail:        site.GetAcmeEmail(),
		CaddyAdminAddr:   caddyAdminAddr(),
		HelmsmanUpstream: envDefault("HELMSMAN_WEBHOOK_URL", "http://localhost:18007"),
		ChandlerUpstream: envDefault("CHANDLER_URL", "chandler:18020"),
		MistUpstream:     envDefault("MISTSERVER_URL", "http://mistserver:8080"),
	}
	// Strip http:// prefix for Caddy reverse_proxy upstream
	params.HelmsmanUpstream = strings.TrimPrefix(params.HelmsmanUpstream, "http://")
	params.MistUpstream = strings.TrimPrefix(params.MistUpstream, "http://")

	if seed.GetTls() != nil {
		params.TLSCertPath = "/etc/frameworks/certs/cert.pem"
		params.TLSKeyPath = "/etc/frameworks/certs/key.pem"
	}

	rendered, err := RenderCaddyfile(params)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to render production Caddyfile")
		return
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rendered)))
	if hash == m.lastCaddyHash && !certChanged {
		return
	}

	if m.reloadCaddy([]byte(rendered)) {
		m.lastCaddyHash = hash
		m.caddyActivated = true
		m.logger.WithField("site_address", site.GetSiteAddress()).Info("Activated production Caddyfile via ConfigSeed")
	}
}

func caddyAdminAddr() string {
	if sock := os.Getenv("CADDY_ADMIN_SOCKET"); sock != "" {
		return "unix/" + sock
	}
	if url := os.Getenv("CADDY_ADMIN_URL"); url != "" {
		return url
	}
	return "localhost:2019"
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// reloadCaddy triggers a Caddy config reload via the admin API.
// If content is provided, it is POSTed directly. Otherwise reads from /etc/caddy/Caddyfile.
//
// Docker: CADDY_ADMIN_SOCKET=/run/caddy/admin.sock (Unix socket, no network exposure)
// Bare metal: CADDY_ADMIN_URL=http://localhost:2019 (loopback only)
// reloadCaddy returns true on success.
func (m *Manager) reloadCaddy(content []byte) bool {
	socketPath := os.Getenv("CADDY_ADMIN_SOCKET")
	adminURL := os.Getenv("CADDY_ADMIN_URL")

	var client *http.Client
	var baseURL string

	switch {
	case socketPath != "":
		client = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		}
		baseURL = "http://caddy"
	case adminURL != "":
		client = &http.Client{Timeout: 5 * time.Second}
		baseURL = strings.TrimRight(adminURL, "/")
	default:
		return false
	}

	body := content
	if body == nil {
		var err error
		body, err = os.ReadFile("/etc/caddy/Caddyfile")
		if err != nil {
			m.logger.WithError(err).Warn("Failed to read Caddyfile for reload")
			return false
		}
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/load", bytes.NewReader(body))
	if err != nil {
		m.logger.WithError(err).Warn("Failed to create Caddy reload request")
		return false
	}
	req.Header.Set("Content-Type", "text/caddyfile")

	resp, err := client.Do(req)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to reload Caddy configuration")
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		m.logger.Info("Caddy configuration reloaded")
		return true
	}
	m.logger.WithField("status", resp.StatusCode).Warn("Caddy reload returned non-200")
	return false
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

	// ThumbVTT - ensure exists for thumbnail sprite VTT output
	if _, ok := existingProtos["ThumbVTT"]; !ok {
		entry := map[string]interface{}{"connector": "ThumbVTT"}
		need = append(need, entry)
	}

	// Remove unwanted protocols (TSRIST is push-system-only, generates warnings)
	unwanted := []string{"TSRIST"}
	var toDelete []map[string]interface{}
	for _, name := range unwanted {
		if _, ok := existingProtos[name]; ok {
			toDelete = append(toDelete, map[string]interface{}{"connector": name})
		}
	}
	if len(toDelete) > 0 {
		if err := m.mistClient.DeleteProtocols(toDelete); err != nil {
			m.logger.WithError(err).Warn("Failed to remove unwanted protocols")
		}
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
	// - https://edge-egress.example.com/view
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
	base := strings.TrimSpace(seed.GetFoghornBalancerBase())
	if base == "" {
		return fmt.Errorf("ConfigSeed missing foghorn_balancer_base; cannot wire MistServer balancer")
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

		// processing+ source is resolved dynamically via STREAM_SOURCE trigger
		if strings.HasPrefix(def.GetName(), "processing+") {
			entry["source"] = ""
		}

		// All stream types use STREAM_PROCESS trigger for per-instance process config.
		// No static processes in wildcard definitions.
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

func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".frameworks-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
