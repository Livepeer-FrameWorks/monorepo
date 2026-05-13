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

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Manager maintains desired vs current MistServer configuration and reconciles them via the Mist API.
type Manager struct {
	mu               sync.Mutex
	mistClient       *mist.Client
	logger           logging.Logger
	lastSeed         *pb.ConfigSeed
	lastAppliedSum   string
	retryTimer       *time.Timer
	retryAttempt     int
	lastCaddyHash    string
	caddyActivated   bool
	ackSender        func(*pb.ControlMessage)
	lastAckedSeedVer uint64
}

// ApplySeedSender is the signature for the function Helmsman uses to send
// ConfigSeedApplyResult back to Foghorn over the existing bidi control
// stream.
type ApplySeedSender func(*pb.ControlMessage)

const (
	maxReconcileRetryDelay = 30 * time.Second
	inertMistSource        = "/tmp/none"
)

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

// ApplySeed stores the latest ConfigSeed and triggers reconcile. The
// optional sender is invoked with a ConfigSeedApplyResult after Helmsman
// has applied the seed's TLS bundles and reloaded Caddy. Pass nil to
// skip ACK (e.g. tests).
func ApplySeed(seed *pb.ConfigSeed, sender ApplySeedSender) {
	if manager == nil || seed == nil {
		return
	}
	manager.mu.Lock()
	manager.lastSeed = seed
	if sender != nil {
		manager.ackSender = sender
	}
	manager.cancelRetryLocked()
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
	ackSender := m.ackSender
	m.mu.Unlock()
	if seed == nil {
		return
	}

	if len(seed.GetCaBundle()) > 0 {
		m.applyCABundle(seed.GetCaBundle())
	}
	m.applyTelemetryConfig(seed.GetTelemetry())

	certChanged := false
	var bundleResults []bundleApplyResult

	// New path: multi-bundle TLS. Used when Foghorn sends ConfigSeed v2.
	if bundles := seed.GetTlsBundles(); len(bundles) > 0 {
		certChanged, bundleResults = m.applyTLSBundles(bundles)
	} else if tls := seed.GetTls(); tls != nil {
		// Legacy path: single TLS bundle. Old-style ConfigSeed.
		certChanged = m.applyTLSBundle(tls)
	} else {
		m.removeTLSBundle()
		m.removeAllBundleFiles()
	}

	caddyReloadOK := true
	if site := seed.GetSite(); site != nil || len(seed.GetTlsBundles()) > 0 {
		caddyReloadOK = m.activateCaddy(seed, certChanged)
	} else if certChanged {
		caddyReloadOK = m.reloadCaddy(nil)
	}

	// Send ConfigSeedApplyResult ACK when this seed carries the
	// multi-bundle set that DNS publishing depends on.
	if ackSender != nil && seed.GetSeedVersion() > 0 && len(seed.GetTlsBundles()) > 0 {
		m.sendApplyResultLocked(seed, bundleResults, caddyReloadOK, ackSender)
	}
	current, err := m.mistClient.ConfigBackup()
	if err != nil {
		m.logger.WithError(err).Warn("ConfigBackup failed, retrying Mist config reconcile")
		m.scheduleRetry()
		return
	}
	desiredConfig := map[string]any{}

	// Location (from seed)
	if seed.GetLatitude() != 0 || seed.GetLongitude() != 0 || seed.GetLocationName() != "" {
		desiredConfig["location"] = map[string]any{
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
	triggers := map[string]any{
		"PUSH_REWRITE": []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/push_rewrite"), "sync": true}},
		// "PLAY_REWRITE":      []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/play_rewrite"), "sync": true, "streams": []string{"vod+", "live+"}}},
		"PLAY_REWRITE":      []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/play_rewrite"), "sync": true}},
		"STREAM_SOURCE":     []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/stream_source"), "sync": true}},
		"PUSH_OUT_START":    []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/push_out_start"), "sync": true}},
		"PUSH_END":          []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/push_end"), "sync": false}},
		"USER_NEW":          []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/user_new"), "sync": true, "default": "true"}},
		"USER_END":          []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/user_end"), "sync": false}},
		"STREAM_BUFFER":     []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/stream_buffer"), "sync": false}},
		"STREAM_END":        []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/stream_end"), "sync": false}},
		"LIVE_TRACK_LIST":   []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/live_track_list"), "sync": false}},
		"RECORDING_END":     []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/recording_end"), "sync": false}},
		"RECORDING_SEGMENT": []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/recording_segment"), "sync": false}},
		// Processing billing triggers (for tracking transcoding usage)
		"LIVEPEER_SEGMENT_COMPLETE":           []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/livepeer_segment_complete"), "sync": false}},
		"PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE": []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/process_av_segment_complete"), "sync": false}},
		"THUMBNAIL_UPDATED":                   []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/thumbnail_updated"), "sync": false}},
		"STREAM_PROCESS":                      []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/stream_process"), "sync": true, "streams": []string{"live+", "processing+", "vod+", "pull+"}}},
		"PROCESS_EXIT":                        []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/process_exit"), "sync": false, "streams": []string{"processing+"}}},
	}
	desiredConfig["triggers"] = triggers

	if err := m.ensureProtocols(current); err != nil {
		m.logger.WithError(err).Warn("ensureProtocols failed")
		m.scheduleRetry()
		return
	}
	if err := m.ensureStreams(seed); err != nil {
		m.logger.WithError(err).Warn("ensureStreams failed")
		m.scheduleRetry()
		return
	}

	if len(desiredConfig) > 0 {
		if _, err := m.mistClient.UpdateConfig(desiredConfig); err != nil {
			m.logger.WithError(err).Warn("UpdateConfig failed")
			m.scheduleRetry()
			return
		}
	}

	if err := m.mistClient.Save(); err != nil {
		m.logger.WithError(err).Warn("Mist config save failed")
		m.scheduleRetry()
		return
	}

	// Record applied signature
	if sum := hashSeed(seed); sum != "" {
		m.mu.Lock()
		m.lastAppliedSum = sum
		m.retryAttempt = 0
		m.mu.Unlock()
	}
}

func (m *Manager) scheduleRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lastSeed == nil || m.retryTimer != nil {
		return
	}

	m.retryAttempt++
	delay := time.Second << min(m.retryAttempt-1, 5)
	if delay > maxReconcileRetryDelay {
		delay = maxReconcileRetryDelay
	}

	m.logger.WithFields(logging.Fields{
		"attempt":  m.retryAttempt,
		"delay_ms": delay.Milliseconds(),
	}).Info("Scheduled Mist config reconcile retry")

	m.retryTimer = time.AfterFunc(delay, func() {
		m.mu.Lock()
		m.retryTimer = nil
		m.mu.Unlock()
		m.reconcile()
	})
}

func (m *Manager) cancelRetryLocked() {
	if m.retryTimer != nil {
		m.retryTimer.Stop()
		m.retryTimer = nil
	}
	m.retryAttempt = 0
}

func grpcCABundlePath() string {
	if path := strings.TrimSpace(os.Getenv("GRPC_TLS_CA_PATH")); path != "" {
		return path
	}
	return "/etc/frameworks/pki/ca.crt"
}

func edgeTLSPaths() (string, string) {
	certPath := strings.TrimSpace(os.Getenv("HELMSMAN_TLS_CERT_PATH"))
	if certPath == "" {
		certPath = "/etc/frameworks/certs/cert.pem"
	}
	keyPath := strings.TrimSpace(os.Getenv("HELMSMAN_TLS_KEY_PATH"))
	if keyPath == "" {
		keyPath = "/etc/frameworks/certs/key.pem"
	}
	return certPath, keyPath
}

// edgeBundleDir returns the directory where per-bundle cert/key files
// are written. Each bundle gets two files in this directory keyed by a
// sanitized bundle_id.
func edgeBundleDir() string {
	if dir := strings.TrimSpace(os.Getenv("HELMSMAN_TLS_BUNDLE_DIR")); dir != "" {
		return dir
	}
	return "/etc/frameworks/certs/bundles"
}

// sanitizeBundleID maps an arbitrary bundle identity (e.g. "tenant:acme",
// "cluster:media-us-1") to a filesystem-safe filename stem.
func sanitizeBundleID(id string) string {
	var sb strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	s := sb.String()
	if s == "" {
		return "bundle"
	}
	return s
}

// edgeBundleTLSPaths returns the cert/key file paths for a given bundle_id.
func edgeBundleTLSPaths(bundleID string) (string, string) {
	stem := sanitizeBundleID(bundleID)
	dir := edgeBundleDir()
	return filepath.Join(dir, stem+".crt"), filepath.Join(dir, stem+".key")
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

// bundleApplyResult records the per-bundle outcome of applyTLSBundles.
type bundleApplyResult struct {
	BundleID string
	Success  bool
	Err      string
}

// applyTLSBundles writes cert/key files for every bundle into the
// per-bundle directory, removes files for bundle_ids no longer in the
// set, and returns per-bundle apply results plus whether any file on
// disk changed.
func (m *Manager) applyTLSBundles(bundles []*pb.TLSCertBundle) (bool, []bundleApplyResult) {
	results := make([]bundleApplyResult, 0, len(bundles))
	anyChanged := false

	dir := edgeBundleDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.logger.WithError(err).Warn("Failed to create TLS bundle directory")
		// Surface the failure per-bundle so Foghorn ACK reflects it.
		for _, b := range bundles {
			results = append(results, bundleApplyResult{
				BundleID: b.GetBundleId(),
				Success:  false,
				Err:      "create bundle dir: " + err.Error(),
			})
		}
		return false, results
	}

	keepFiles := make(map[string]struct{}, len(bundles)*2)
	for _, bundle := range bundles {
		bundleID := bundle.GetBundleId()
		if bundleID == "" {
			results = append(results, bundleApplyResult{
				BundleID: "",
				Success:  false,
				Err:      "empty bundle_id",
			})
			continue
		}
		certPath, keyPath := edgeBundleTLSPaths(bundleID)
		keepFiles[filepath.Base(certPath)] = struct{}{}
		keepFiles[filepath.Base(keyPath)] = struct{}{}

		changed, err := writeBundleFiles(certPath, keyPath, bundle)
		if err != nil {
			results = append(results, bundleApplyResult{
				BundleID: bundleID,
				Success:  false,
				Err:      err.Error(),
			})
			continue
		}
		if changed {
			anyChanged = true
			m.logger.WithFields(logging.Fields{
				"bundle_id":  bundleID,
				"domain":     bundle.GetDomain(),
				"expires_at": bundle.GetExpiresAt(),
			}).Info("Applied TLS bundle from ConfigSeed")
		}
		results = append(results, bundleApplyResult{BundleID: bundleID, Success: true})
	}

	// Clean up files for bundles no longer in the seed.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".crt") && !strings.HasSuffix(name, ".key") {
				continue
			}
			if _, ok := keepFiles[name]; ok {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err == nil {
				anyChanged = true
				m.logger.WithField("file", name).Info("Removed stale TLS bundle file")
			}
		}
	}

	return anyChanged, results
}

// writeBundleFiles atomically writes cert + key for one bundle. Returns
// true if either file content changed.
func writeBundleFiles(certPath, keyPath string, bundle *pb.TLSCertBundle) (bool, error) {
	certBytes := []byte(bundle.GetCertPem())
	keyBytes := []byte(bundle.GetKeyPem())
	if len(certBytes) == 0 || len(keyBytes) == 0 {
		return false, fmt.Errorf("empty cert or key")
	}

	certSame := false
	if existing, err := os.ReadFile(certPath); err == nil && bytes.Equal(existing, certBytes) {
		certSame = true
	}
	keySame := false
	if existing, err := os.ReadFile(keyPath); err == nil && bytes.Equal(existing, keyBytes) {
		keySame = true
	}
	if certSame && keySame {
		return false, nil
	}

	certTmp, err := writeManagedFileTemp(certPath, certBytes, 0o644)
	if err != nil {
		return false, fmt.Errorf("stage cert: %w", err)
	}
	defer func() { removeIfNotEmpty(certTmp) }()

	keyTmp, err := writeManagedFileTemp(keyPath, keyBytes, 0o640)
	if err != nil {
		return false, fmt.Errorf("stage key: %w", err)
	}
	defer func() { removeIfNotEmpty(keyTmp) }()

	if err := os.Rename(keyTmp, keyPath); err != nil {
		return false, fmt.Errorf("install key: %w", err)
	}
	keyTmp = ""
	if err := os.Rename(certTmp, certPath); err != nil {
		return false, fmt.Errorf("install cert: %w", err)
	}
	certTmp = ""
	return true, nil
}

// removeAllBundleFiles deletes everything in the per-bundle dir. Used
// when the seed has no bundles at all.
func (m *Manager) removeAllBundleFiles() {
	dir := edgeBundleDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// sendApplyResultLocked composes a ConfigSeedApplyResult from per-bundle
// results and the Caddy reload outcome, and dispatches via the sender.
// Only sends once per seed_version (idempotent on retries).
func (m *Manager) sendApplyResultLocked(seed *pb.ConfigSeed, results []bundleApplyResult, caddyOK bool, sender func(*pb.ControlMessage)) {
	if sender == nil {
		return
	}
	m.mu.Lock()
	if seed.GetSeedVersion() <= m.lastAckedSeedVer {
		m.mu.Unlock()
		return
	}
	m.lastAckedSeedVer = seed.GetSeedVersion()
	m.mu.Unlock()

	applied := make([]string, 0, len(results))
	failed := make([]string, 0)
	var firstErr string
	allOK := true
	for _, r := range results {
		if r.Success {
			applied = append(applied, r.BundleID)
		} else {
			failed = append(failed, r.BundleID)
			if firstErr == "" {
				firstErr = r.Err
			}
			allOK = false
		}
	}
	if !caddyOK {
		// Reload did not happen; files are on disk but Caddy is still
		// serving the old config. No bundle is actually "applied" from
		// the perspective of TLS termination, so demote the per-file
		// successes to failures. Foghorn's DNS gate must see this as
		// a non-ready state.
		allOK = false
		if firstErr == "" {
			firstErr = "caddy reload failed"
		}
		failed = append(failed, applied...)
		applied = applied[:0]
	}

	ack := &pb.ConfigSeedApplyResult{
		NodeId:           seed.GetNodeId(),
		SeedVersion:      seed.GetSeedVersion(),
		AppliedBundleIds: applied,
		FailedBundleIds:  failed,
		Success:          allOK,
		Error:            firstErr,
		AppliedAt:        timestamppb.Now(),
	}
	sender(&pb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &pb.ControlMessage_ConfigSeedApplyResult{
			ConfigSeedApplyResult: ack,
		},
	})
}

// applyTLSBundle writes cert/key files to disk. Returns true if files were changed.
func (m *Manager) applyTLSBundle(bundle *pb.TLSCertBundle) bool {
	certPath, keyPath := edgeTLSPaths()

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

	certTmp, err := writeManagedFileTemp(certPath, certBytes, 0o644)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to stage TLS certificate file")
		return false
	}
	defer func() { removeIfNotEmpty(certTmp) }()

	keyTmp, err := writeManagedFileTemp(keyPath, keyBytes, 0o640)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to stage TLS key file")
		return false
	}
	defer func() { removeIfNotEmpty(keyTmp) }()

	if err := os.Rename(keyTmp, keyPath); err != nil {
		m.logger.WithError(err).Warn("Failed to install TLS key file")
		return false
	}
	keyTmp = ""
	if err := os.Rename(certTmp, certPath); err != nil {
		m.logger.WithError(err).Warn("Failed to install TLS certificate file")
		return false
	}
	certTmp = ""

	m.logger.WithFields(logging.Fields{
		"domain":     bundle.GetDomain(),
		"expires_at": bundle.GetExpiresAt(),
	}).Info("Applied TLS certificate bundle from ConfigSeed")

	return true
}

func (m *Manager) removeTLSBundle() {
	certPath, keyPath := edgeTLSPaths()

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

func writeManagedFileTemp(path string, data []byte, mode os.FileMode) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

func removeIfNotEmpty(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// activateCaddy renders the production Caddyfile from ConfigSeed and pushes it to Caddy.
// Returns true on a successful reload (or no-op when config is unchanged
// and no cert change was detected). Returns false if rendering or reload
// failed; callers ACK accordingly.
func (m *Manager) activateCaddy(seed *pb.ConfigSeed, certChanged bool) bool {
	bundles := composeCaddyBundles(seed)
	if len(bundles) == 0 {
		// Nothing to render: no bundles AND no legacy site address.
		if certChanged {
			return m.reloadCaddy(nil)
		}
		return true
	}

	params := CaddyfileParams{
		Bundles:          bundles,
		CaddyAdminAddr:   caddyAdminAddr(),
		HelmsmanUpstream: envDefault("HELMSMAN_WEBHOOK_URL", "http://localhost:18007"),
		ChandlerUpstream: envDefault("CHANDLER_URL", "chandler:18020"),
		MistUpstream:     envDefault("MISTSERVER_URL", "http://mistserver:8080"),
	}
	if site := seed.GetSite(); site != nil {
		params.AcmeEmail = site.GetAcmeEmail()
	}
	// Strip http:// prefix for Caddy reverse_proxy upstream
	params.HelmsmanUpstream = strings.TrimPrefix(params.HelmsmanUpstream, "http://")
	params.MistUpstream = strings.TrimPrefix(params.MistUpstream, "http://")

	rendered, err := RenderCaddyfile(params)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to render production Caddyfile")
		return false
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rendered)))
	if hash == m.lastCaddyHash && !certChanged {
		return true
	}

	if m.reloadCaddy([]byte(rendered)) {
		m.lastCaddyHash = hash
		m.caddyActivated = true
		m.logger.WithField("bundle_count", len(bundles)).Info("Activated production Caddyfile via ConfigSeed")
		return true
	}
	return false
}

// composeCaddyBundles builds the list of CaddyfileBundle entries from a
// ConfigSeed. Multi-bundle TLS is authoritative when present; otherwise
// a single SiteConfig/Tls pair renders one site block.
func composeCaddyBundles(seed *pb.ConfigSeed) []CaddyfileBundle {
	if bundles := seed.GetTlsBundles(); len(bundles) > 0 {
		out := make([]CaddyfileBundle, 0, len(bundles))
		for _, b := range bundles {
			addrs := b.GetSiteAddresses()
			if len(addrs) == 0 {
				// Bundle without explicit site addresses: skip; cannot
				// render a Caddy block without a hostname.
				continue
			}
			cert, key := edgeBundleTLSPaths(b.GetBundleId())
			out = append(out, CaddyfileBundle{
				SiteAddress: strings.Join(addrs, " "),
				TLSCertPath: cert,
				TLSKeyPath:  key,
			})
		}
		return out
	}
	// Single-bundle seed: one site block from SiteConfig + Tls.
	site := seed.GetSite()
	if site == nil || site.GetSiteAddress() == "" {
		return nil
	}
	b := CaddyfileBundle{SiteAddress: site.GetSiteAddress()}
	if seed.GetTls() != nil {
		b.TLSCertPath, b.TLSKeyPath = edgeTLSPaths()
	}
	return []CaddyfileBundle{b}
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

func (m *Manager) ensureProtocols(current map[string]any) error {
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
	existingProtos := map[string]map[string]any{}
	if cfg, ok := current["config"].(map[string]any); ok {
		if protos, ok := cfg["protocols"].([]any); ok {
			for _, p := range protos {
				if pm, ok := p.(map[string]any); ok {
					if name, ok := pm["connector"].(string); ok && name != "" {
						existingProtos[name] = pm
					}
				}
			}
		}
	}

	need := []map[string]any{}
	var protocolUpdates []protocolUpdate

	// HTTP protocol - check if exists and has correct pubaddr
	if existing, ok := existingProtos["HTTP"]; !ok {
		entry := map[string]any{"connector": "HTTP"}
		if httpPubURL != "" {
			entry["pubaddr"] = []string{httpPubURL}
		}
		need = append(need, entry)
	} else if httpPubURL != "" {
		// Check if pubaddr needs updating
		currentPubaddr := ""
		if pa, ok := existing["pubaddr"].([]any); ok && len(pa) > 0 {
			if s, ok := pa[0].(string); ok {
				currentPubaddr = s
			}
		}
		if currentPubaddr != httpPubURL {
			m.logger.WithFields(logging.Fields{
				"current": currentPubaddr,
				"desired": httpPubURL,
			}).Info("HTTP pubaddr needs update")
			updated := cloneStringAnyMap(existing)
			updated["pubaddr"] = []string{httpPubURL}
			protocolUpdates = append(protocolUpdates, protocolUpdate{old: existing, new: updated})
		}
	}

	// WebRTC protocol - check if exists and has correct pubhost
	if existing, ok := existingProtos["WebRTC"]; !ok {
		entry := map[string]any{"connector": "WebRTC", "bindhost": "0.0.0.0"}
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
			updated := cloneStringAnyMap(existing)
			updated["bindhost"] = "0.0.0.0"
			updated["pubhost"] = webrtcPubHost
			protocolUpdates = append(protocolUpdates, protocolUpdate{old: existing, new: updated})
		}
	}

	// DTSC - ensure exists for inter-node communication
	if _, ok := existingProtos["DTSC"]; !ok {
		entry := map[string]any{"connector": "DTSC"}
		need = append(need, entry)
	}

	// WSRaw - ensure exists for WebCodecs playback
	if _, ok := existingProtos["WSRaw"]; !ok {
		entry := map[string]any{"connector": "WSRaw"}
		need = append(need, entry)
	}

	// ThumbVTT - ensure exists for thumbnail sprite VTT output
	if _, ok := existingProtos["ThumbVTT"]; !ok {
		entry := map[string]any{"connector": "ThumbVTT"}
		need = append(need, entry)
	}

	// CMAF - serves LL-HLS, DASH, and HSS over fMP4. nonchunked forces
	// per-segment buffering so segment responses carry Content-Length, which
	// is required for player compatibility across dash.js and hls.js LL-HLS.
	cmafNeedsUpdate := false
	if existing, ok := existingProtos["CMAF"]; !ok {
		entry := map[string]any{"connector": "CMAF", "mergesessions": true, "nonchunked": true}
		need = append(need, entry)
	} else {
		currentMergesessions, hasMergesessions := existing["mergesessions"].(bool)
		currentNonchunked, nok := existing["nonchunked"].(bool)
		if !hasMergesessions || !currentMergesessions || !nok || !currentNonchunked {
			m.logger.WithFields(logging.Fields{
				"current_mergesessions": currentMergesessions,
				"desired_mergesessions": true,
				"current_nonchunked":    currentNonchunked,
				"desired_nonchunked":    true,
			}).Info("CMAF settings need update")
			cmafNeedsUpdate = true
		}
		if cmafNeedsUpdate {
			updated := cloneStringAnyMap(existing)
			updated["mergesessions"] = true
			updated["nonchunked"] = true
			protocolUpdates = append(protocolUpdates, protocolUpdate{old: existing, new: updated})
		}
	}

	// Remove unwanted protocols (TSRIST is push-system-only, generates warnings)
	unwanted := []string{"TSRIST"}
	var toDelete []map[string]any
	for _, name := range unwanted {
		if _, ok := existingProtos[name]; ok {
			toDelete = append(toDelete, map[string]any{"connector": name})
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

	for _, update := range protocolUpdates {
		if err := m.mistClient.UpdateProtocol(update.old, update.new); err != nil {
			return err
		}
	}
	if len(protocolUpdates) > 0 {
		m.logger.WithField("count", len(protocolUpdates)).Info("Updated protocol settings")
	}

	return nil
}

type protocolUpdate struct {
	old map[string]any
	new map[string]any
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
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
	pushSource := "balance:" + base + "?fallback=push://"
	pullSource := "balance:" + base

	streams := map[string]map[string]any{}
	for _, t := range seed.GetTemplates() {
		def := t.GetDef()
		if def == nil || def.GetName() == "" {
			continue
		}
		source := pushSource
		if def.GetName() == "pull" {
			source = pullSource
		}
		entry := map[string]any{
			"name":          def.GetName(),
			"source":        source,
			"realtime":      def.GetRealtime(),
			"stop_sessions": def.GetStopSessions(),
			"tags":          def.GetTags(),
		}

		// processing+ sources are resolved dynamically via STREAM_SOURCE, but
		// Mist still requires a syntactically valid configured source.
		if def.GetName() == "processing" || strings.HasPrefix(def.GetName(), "processing+") {
			entry["source"] = inertMistSource
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
