package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Manager maintains desired vs current MistServer configuration and reconciles them via the Mist API.
type Manager struct {
	mu               sync.Mutex
	reconcileMu      sync.Mutex
	mistClient       mistAPI
	logger           logging.Logger
	lastSeed         *ipcpb.ConfigSeed
	lastAppliedSum   string
	retryTimer       *time.Timer
	retryAttempt     int
	driftRepairOnce  sync.Once
	lastCaddyHash    string
	caddyActivated   bool
	ackSender        func(*ipcpb.ControlMessage)
	lastAckedSeedVer uint64
}

type mistAPI interface {
	ConfigBackup() (map[string]interface{}, error)
	UpdateConfig(partial map[string]interface{}) (map[string]interface{}, error)
	Save() error
	AddProtocols(protocols []map[string]interface{}) error
	UpdateProtocol(oldConfig, newConfig map[string]interface{}) error
	DeleteProtocols(protocols []map[string]interface{}) error
	AddStreams(streams map[string]map[string]interface{}) error
	DeleteStreams(names []string) error
}

// ApplySeedSender is the signature for the function Helmsman uses to send
// ConfigSeedApplyResult back to Foghorn over the existing bidi control
// stream.
type ApplySeedSender func(*ipcpb.ControlMessage)

const (
	maxReconcileRetryDelay = 30 * time.Second
	mistConfigRepairEvery  = 30 * time.Second
	inertMistSource        = "/tmp/none"
	edgeBundleDirMode      = fs.ModeSetgid | 0o770
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
func ApplySeed(seed *ipcpb.ConfigSeed, sender ApplySeedSender) {
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
	manager.startDriftRepairLoop()
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
func GetOperationalMode() ipcpb.NodeOperationalMode {
	if manager == nil {
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.lastSeed == nil {
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
	mode := manager.lastSeed.GetOperationalMode()
	if mode == ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED {
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
	return mode
}

// reconcile computes desired config from seed + env and applies minimal changes idempotently.
func (m *Manager) reconcile() {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()

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
		if !caddyReloadOK {
			m.scheduleRetry()
		}
	} else if certChanged {
		caddyReloadOK = m.reloadCaddy(nil)
		if !caddyReloadOK {
			m.scheduleRetry()
		}
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
	applyBaselineMistConfig(desiredConfig)

	// Location (from seed)
	if seed.GetLatitude() != 0 || seed.GetLongitude() != 0 || seed.GetLocationName() != "" {
		desiredConfig["location"] = map[string]any{
			"lat":  math.Round(seed.GetLatitude()*1e4) / 1e4,
			"lon":  math.Round(seed.GetLongitude()*1e4) / 1e4,
			"name": seed.GetLocationName(),
		}
	}

	if bwLimit := ConfiguredBandwidthLimitBytesPerSec(); bwLimit > 0 {
		desiredConfig["bwlimit"] = bwLimit
	}

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
		"PUSH_INPUT_CLOSE":  []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/push_input_close"), "sync": false}},
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
		"STREAM_PROCESS":                      []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/stream_process"), "sync": true}},
		"PROCESS_EXIT":                        []any{map[string]any{"handler": join(webhookBase, "/webhooks/mist/process_exit"), "sync": false, "streams": []string{"processing+"}}},
	}
	desiredConfig["triggers"] = triggers

	if err := m.ensureProtocols(current); err != nil {
		m.logger.WithError(err).Warn("ensureProtocols failed")
		m.scheduleRetry()
		return
	}
	if err := m.ensureStreams(current, seed); err != nil {
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

	if err := m.repairMissingManagedStreams(seed); err != nil {
		m.logger.WithError(err).Warn("Mist stream template verification failed")
		m.scheduleRetry()
		return
	}

	m.mu.Lock()
	recoveredAfterAttempts := m.retryAttempt
	m.mu.Unlock()
	fields := logging.Fields{
		"node_id":        seed.GetNodeId(),
		"template_count": len(seed.GetTemplates()),
	}
	if recoveredAfterAttempts > 0 {
		fields["recovered_after_attempts"] = recoveredAfterAttempts
	}
	m.logger.WithFields(fields).Info("Mist config reconcile succeeded")

	// Record applied signature
	if sum := hashSeed(seed); sum != "" {
		m.mu.Lock()
		m.lastAppliedSum = sum
		m.retryAttempt = 0
		m.mu.Unlock()
	}
}

func applyBaselineMistConfig(desiredConfig map[string]any) {
	desiredConfig["accesslog"] = "LOG"
	desiredConfig["debug"] = 4
	desiredConfig["prometheus"] = mist.MetricsConfigValue
	desiredConfig["sessionInputMode"] = 15
	desiredConfig["sessionOutputMode"] = 15
	desiredConfig["sessionStreamInfoMode"] = "1"
	desiredConfig["sessionUnspecifiedMode"] = 0
	desiredConfig["sessionViewerMode"] = 14
	desiredConfig["tknMode"] = 15
	desiredConfig["trustedproxy"] = []string{"127.0.0.1", "::1", "localhost", "nginx"}
}

func (m *Manager) startDriftRepairLoop() {
	m.driftRepairOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(mistConfigRepairEvery)
			defer ticker.Stop()
			for range ticker.C {
				m.repairConfigDrift()
			}
		}()
	})
}

func (m *Manager) repairConfigDrift() {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()

	m.mu.Lock()
	seed := m.lastSeed
	m.mu.Unlock()
	if seed == nil {
		return
	}
	if err := m.repairMissingManagedStreams(seed); err != nil {
		m.logger.WithError(err).Warn("Mist managed stream repair failed")
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

func (m *Manager) applyTelemetryConfig(cfg *commonpb.EdgeTelemetryConfig) bool {
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

	caTmp, err := writeManagedFileTemp(caPath, bundle, 0o644)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to stage gRPC CA bundle")
		return false
	}
	defer func() { removeIfNotEmpty(caTmp) }()
	if err := os.Rename(caTmp, caPath); err != nil {
		m.logger.WithError(err).Warn("Failed to install gRPC CA bundle")
		return false
	}
	caTmp = ""
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
func (m *Manager) applyTLSBundles(bundles []*ipcpb.TLSCertBundle) (bool, []bundleApplyResult) {
	results := make([]bundleApplyResult, 0, len(bundles))
	anyChanged := false

	dir := edgeBundleDir()
	if err := os.MkdirAll(dir, edgeBundleDirMode.Perm()); err != nil {
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
	if err := os.Chmod(dir, edgeBundleDirMode); err != nil {
		m.logger.WithError(err).WithField("path", dir).Warn("Failed to set TLS bundle directory mode")
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
func writeBundleFiles(certPath, keyPath string, bundle *ipcpb.TLSCertBundle) (bool, error) {
	certBytes := []byte(bundle.GetCertPem())
	keyBytes := []byte(bundle.GetKeyPem())
	if len(certBytes) == 0 || len(keyBytes) == 0 {
		return false, fmt.Errorf("empty cert or key")
	}

	if managedFileUpToDate(certPath, certBytes, 0o644) && managedFileUpToDate(keyPath, keyBytes, 0o640) {
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
	if err := repairManagedFileMetadata(certPath, 0o644); err != nil {
		return false, fmt.Errorf("repair cert metadata: %w", err)
	}
	if err := repairManagedFileMetadata(keyPath, 0o640); err != nil {
		return false, fmt.Errorf("repair key metadata: %w", err)
	}
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
func (m *Manager) sendApplyResultLocked(seed *ipcpb.ConfigSeed, results []bundleApplyResult, caddyOK bool, sender func(*ipcpb.ControlMessage)) {
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

	ack := &ipcpb.ConfigSeedApplyResult{
		NodeId:           seed.GetNodeId(),
		SeedVersion:      seed.GetSeedVersion(),
		AppliedBundleIds: applied,
		FailedBundleIds:  failed,
		Success:          allOK,
		Error:            firstErr,
		AppliedAt:        timestamppb.Now(),
	}
	sender(&ipcpb.ControlMessage{
		SentAt: timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_ConfigSeedApplyResult{
			ConfigSeedApplyResult: ack,
		},
	})
}

// applyTLSBundle writes cert/key files to disk. Returns true if files were changed.
func (m *Manager) applyTLSBundle(bundle *ipcpb.TLSCertBundle) bool {
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

	if managedFileUpToDate(certPath, certBytes, 0o644) && managedFileUpToDate(keyPath, keyBytes, 0o640) {
		return false
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
	if err := repairManagedFileMetadata(certPath, 0o644); err != nil {
		m.logger.WithError(err).Warn("Failed to repair TLS certificate metadata")
		return false
	}
	if err := repairManagedFileMetadata(keyPath, 0o640); err != nil {
		m.logger.WithError(err).Warn("Failed to repair TLS key metadata")
		return false
	}

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

func managedFileUpToDate(path string, data []byte, mode os.FileMode) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return false
	}
	targetGID, ok := caddyReadableGroupID(filepath.Dir(path))
	if !ok || !sameGroup(info, targetGID) {
		return false
	}
	existing, err := os.ReadFile(path)
	return err == nil && bytes.Equal(existing, data)
}

func repairManagedFileMetadata(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	targetGID, ok := caddyReadableGroupID(filepath.Dir(path))
	if !ok {
		return fmt.Errorf("resolve caddy-readable group for %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if sameGroup(info, targetGID) {
		return nil
	}
	return os.Chown(path, -1, targetGID)
}

func caddyReadableGroupID(parentDir string) (int, bool) {
	if groupName := strings.TrimSpace(os.Getenv("CADDY_TLS_GROUP")); groupName != "" {
		if gid, err := strconv.Atoi(groupName); err == nil {
			return gid, true
		}
		if gid, ok := lookupGroupID(groupName); ok {
			return gid, true
		}
	}
	if gid, ok := lookupGroupID("caddy"); ok {
		return gid, true
	}
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		return 0, false
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(parentStat.Gid), true
}

func lookupGroupID(name string) (int, bool) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, false
	}
	return gid, true
}

func sameGroup(fileInfo os.FileInfo, gid int) bool {
	fileStat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(fileStat.Gid) == gid
}

func removeIfNotEmpty(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// activateCaddy renders the production Caddyfile from ConfigSeed and pushes it to Caddy.
// It always reloads the rendered config because Caddy may have been restarted
// independently after Helmsman last activated it.
// Returns false if rendering, persistence, or reload failed; callers ACK accordingly.
func (m *Manager) activateCaddy(seed *ipcpb.ConfigSeed, certChanged bool) bool {
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
		CaddyAdminAddr:   caddyfileAdminAddr(),
		HelmsmanUpstream: envDefault("HELMSMAN_WEBHOOK_URL", "http://localhost:18007"),
		ChandlerUpstream: envDefault("CHANDLER_URL", "chandler:18020"),
		MistUpstream:     envDefault("MISTSERVER_HTTP_URL", "http://mistserver:8080"),
	}
	if site := seed.GetSite(); site != nil {
		params.AcmeEmail = site.GetAcmeEmail()
		params.EdgeDomain = site.GetEdgeDomain()
	}
	// Strip http:// prefix for Caddy reverse_proxy upstream
	params.HelmsmanUpstream = strings.TrimPrefix(params.HelmsmanUpstream, "http://")
	params.MistUpstream = strings.TrimPrefix(params.MistUpstream, "http://")

	rendered, err := RenderCaddyfile(params)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to render production Caddyfile")
		return false
	}
	if err := m.persistCaddyfile([]byte(rendered)); err != nil {
		m.logger.WithError(err).WithField("path", caddyConfigPath()).Warn("Failed to persist production Caddyfile")
		return false
	}
	if err := verifyCaddyTLSFiles(bundles); err != nil {
		m.logger.WithError(err).Warn("Caddy TLS bundle preflight failed")
		return false
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rendered)))
	logActivation := hash != m.lastCaddyHash || certChanged || !m.caddyActivated
	if m.reloadCaddy([]byte(rendered)) {
		m.lastCaddyHash = hash
		m.caddyActivated = true
		if logActivation {
			m.logger.WithField("bundle_count", len(bundles)).Info("Activated production Caddyfile via ConfigSeed")
		}
		return true
	}
	return false
}

func (m *Manager) persistCaddyfile(content []byte) error {
	path := caddyConfigPath()
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	tmpPath, err := writeManagedFileTemp(path, content, 0o644)
	if err != nil {
		return err
	}
	defer func() { removeIfNotEmpty(tmpPath) }()
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	tmpPath = ""
	return nil
}

// composeCaddyBundles builds the list of CaddyfileBundle entries from a
// ConfigSeed. Multi-bundle TLS is authoritative when present; otherwise
// a single SiteConfig/Tls pair renders one site block.
func composeCaddyBundles(seed *ipcpb.ConfigSeed) []CaddyfileBundle {
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
		if site := seed.GetSite(); site != nil {
			edgeDomain := strings.TrimSpace(site.GetEdgeDomain())
			if edgeDomain != "" && !caddyBundlesCoverHost(out, edgeDomain) {
				out = append(out, CaddyfileBundle{SiteAddress: edgeDomain})
			}
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

func caddyBundlesCoverHost(bundles []CaddyfileBundle, host string) bool {
	host = strings.Trim(strings.TrimSpace(host), ".")
	if host == "" {
		return false
	}
	for _, b := range bundles {
		for _, addr := range strings.Fields(b.SiteAddress) {
			addr = strings.Trim(strings.TrimSpace(addr), ".")
			if addr == host {
				return true
			}
			if strings.HasPrefix(addr, "*.") {
				suffix := strings.TrimPrefix(addr, "*.")
				if strings.HasSuffix(host, "."+suffix) {
					prefix := strings.TrimSuffix(host, "."+suffix)
					if prefix != "" && !strings.Contains(prefix, ".") {
						return true
					}
				}
			}
		}
	}
	return false
}

func verifyCaddyTLSFiles(bundles []CaddyfileBundle) error {
	for _, bundle := range bundles {
		if strings.TrimSpace(bundle.TLSCertPath) == "" && strings.TrimSpace(bundle.TLSKeyPath) == "" {
			continue
		}
		if err := verifyCaddyTLSFile(bundle.TLSCertPath, 0o644, false); err != nil {
			return fmt.Errorf("cert %s: %w", bundle.TLSCertPath, err)
		}
		if err := verifyCaddyTLSFile(bundle.TLSKeyPath, 0o640, true); err != nil {
			return fmt.Errorf("key %s: %w", bundle.TLSKeyPath, err)
		}
	}
	return nil
}

func verifyCaddyTLSFile(path string, wantMode os.FileMode, requireGroupRead bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if mode := info.Mode().Perm(); mode != wantMode {
		return fmt.Errorf("mode %o, want %o", mode, wantMode)
	}
	if requireGroupRead && info.Mode().Perm()&0o040 == 0 {
		return fmt.Errorf("group read bit is not set")
	}
	targetGID, ok := caddyReadableGroupID(filepath.Dir(path))
	if !ok {
		return fmt.Errorf("resolve caddy-readable group")
	}
	if !sameGroup(info, targetGID) {
		return fmt.Errorf("group is not caddy-readable")
	}
	return nil
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

func caddyfileAdminAddr() string {
	raw := caddyAdminAddr()
	if strings.HasPrefix(raw, "unix/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func caddyConfigPath() string {
	return envDefault("CADDY_CONFIG_PATH", "/etc/caddy/Caddyfile")
}

// reloadCaddy triggers a Caddy config reload via the admin API.
// If content is provided, it is POSTed directly. Otherwise reads from CADDY_CONFIG_PATH.
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
		body, err = os.ReadFile(caddyConfigPath())
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
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		m.logger.WithError(readErr).Warn("Failed to read Caddy reload response body")
		return false
	}
	bodyText := strings.TrimSpace(string(respBody))

	if resp.StatusCode != http.StatusOK {
		fields := logging.Fields{"status": resp.StatusCode}
		if bodyText != "" {
			fields["response"] = bodyText
		}
		m.logger.WithFields(fields).Warn("Caddy reload returned non-200")
		return false
	}
	if bodyText != "" {
		if isCaddyLoadWarningBody(bodyText) {
			m.logger.WithField("response", bodyText).Warn("Caddy reload returned adapter warnings")
			m.logger.Info("Caddy configuration reloaded")
			return true
		}
		m.logger.WithField("response", bodyText).Warn("Caddy reload returned an unexpected body")
		return false
	}
	m.logger.Info("Caddy configuration reloaded")
	return true
}

func isCaddyLoadWarningBody(bodyText string) bool {
	var warnings []struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(bodyText), &warnings); err != nil {
		return false
	}
	if len(warnings) == 0 {
		return false
	}
	for _, warning := range warnings {
		if strings.TrimSpace(warning.Message) == "" {
			return false
		}
	}
	return true
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

	for _, desired := range managedProtocolDefinitions(httpPubURL, webrtcPubHost) {
		connector, ok := desired["connector"].(string)
		if !ok || connector == "" {
			continue
		}

		existing, ok := existingProtos[connector]
		if !ok {
			need = append(need, desired)
			continue
		}

		if protocolUpdateNeeded(existing, desired) {
			m.logger.WithFields(logging.Fields{
				"connector": connector,
			}).Info("Mist protocol settings need update")
			updated := cloneStringAnyMap(existing)
			for key, value := range desired {
				updated[key] = value
			}
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

func managedProtocolDefinitions(httpPubURL, webrtcPubHost string) []map[string]any {
	http := map[string]any{"connector": "HTTP", "default_track_sorting": "id_lth"}
	if httpPubURL != "" {
		http["pubaddr"] = []string{httpPubURL}
	}

	webrtc := map[string]any{
		"bindhost":              "0.0.0.0",
		"connector":             "WebRTC",
		"default_track_sorting": "id_lth",
		"jitterlog":             false,
		"mergesessions":         false,
		"nackdisable":           false,
		"nolocal":               false,
		"noresolve":             false,
		"packetlog":             false,
	}
	if webrtcPubHost != "" {
		webrtc["pubhost"] = webrtcPubHost
	}

	return []map[string]any{
		{"connector": "AAC"},
		{"connector": "CMAF", "mergesessions": true, "nonchunked": true},
		{"connector": "DTSC"},
		{"connector": "EBML"},
		{"connector": "FLAC"},
		{"connector": "FLV"},
		{"connector": "H264"},
		{"connector": "HDS"},
		{"connector": "HLS"},
		http,
		{"connector": "HTTPTS"},
		{"connector": "JSON"},
		{"connector": "MP3"},
		{"connector": "MP4"},
		{"connector": "OGG"},
		{"connector": "RTMP"},
		{"connector": "RTSP"},
		{"connector": "SDP"},
		{"connector": "SubRip"},
		{"connector": "TSSRT"},
		{"connector": "WAV"},
		webrtc,
		{"connector": "JPG"},
		{"connector": "WSRaw"},
		{"connector": "ThumbVTT"},
	}
}

func protocolUpdateNeeded(existing, desired map[string]any) bool {
	for key, desiredValue := range desired {
		if !protocolValuesEqual(existing[key], desiredValue) {
			return true
		}
	}
	return false
}

func protocolValuesEqual(existing, desired any) bool {
	desiredStrings, ok := desired.([]string)
	if !ok {
		return reflect.DeepEqual(existing, desired)
	}

	switch existingTyped := existing.(type) {
	case []string:
		return reflect.DeepEqual(existingTyped, desiredStrings)
	case []any:
		if len(existingTyped) != len(desiredStrings) {
			return false
		}
		for i, value := range existingTyped {
			if value != desiredStrings[i] {
				return false
			}
		}
		return true
	default:
		return false
	}
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

func (m *Manager) ensureStreams(current map[string]any, seed *ipcpb.ConfigSeed) error {
	if seed == nil || len(seed.GetTemplates()) == 0 {
		return nil
	}
	base := strings.TrimSpace(seed.GetFoghornBalancerBase())
	if base == "" {
		return fmt.Errorf("ConfigSeed missing foghorn_balancer_base; cannot wire MistServer balancer")
	}
	if stale := staleManagedWildcardStreams(current); len(stale) > 0 {
		if err := m.mistClient.DeleteStreams(stale); err != nil {
			return fmt.Errorf("delete stale wildcard streams: %w", err)
		}
	}
	streams := streamConfigsFromSeed(seed, base, os.Getenv("NODE_ID"))
	if len(streams) == 0 {
		return nil
	}
	return m.mistClient.AddStreams(streams)
}

func (m *Manager) repairMissingManagedStreams(seed *ipcpb.ConfigSeed) error {
	if seed == nil || len(seed.GetTemplates()) == 0 {
		return nil
	}
	base := strings.TrimSpace(seed.GetFoghornBalancerBase())
	if base == "" {
		return fmt.Errorf("ConfigSeed missing foghorn_balancer_base; cannot verify MistServer streams")
	}
	expected := streamConfigsFromSeed(seed, base, os.Getenv("NODE_ID"))
	if len(expected) == 0 {
		return nil
	}
	current, err := m.mistClient.ConfigBackup()
	if err != nil {
		return fmt.Errorf("config backup: %w", err)
	}
	missing := missingManagedStreams(current, expected)
	if len(missing) == 0 {
		return nil
	}

	m.logger.WithFields(logging.Fields{
		"missing": missing,
		"count":   len(missing),
	}).Warn("Mist config missing managed stream templates; repairing")
	if addErr := m.mistClient.AddStreams(expected); addErr != nil {
		return fmt.Errorf("add managed streams: %w", addErr)
	}
	if saveErr := m.mistClient.Save(); saveErr != nil {
		return fmt.Errorf("save managed stream repair: %w", saveErr)
	}

	current, err = m.mistClient.ConfigBackup()
	if err != nil {
		return fmt.Errorf("verify repaired streams: %w", err)
	}
	if stillMissing := missingManagedStreams(current, expected); len(stillMissing) > 0 {
		return fmt.Errorf("managed stream templates still missing after repair: %s", strings.Join(stillMissing, ","))
	}
	return nil
}

func streamConfigsFromSeed(seed *ipcpb.ConfigSeed, base, nodeID string) map[string]map[string]any {
	// Both live and pull wildcards use balance:<foghorn> — the source
	// resolution differs (live falls back to push:// for ingest, pull
	// returns the upstream URI for allowed clusters) but the template
	// shape is identical. The per-type terminal answer is decided by
	// Foghorn's /source dispatch, not by template query params.
	sourceBase := sourceBalancerBase(base, nodeID)
	pushSource := "balance:" + sourceBase
	pullSource := "balance:" + sourceBase

	streams := map[string]map[string]any{}
	for _, t := range seed.GetTemplates() {
		def := t.GetDef()
		if def == nil || def.GetName() == "" {
			continue
		}
		if strings.Contains(def.GetName(), "+") {
			continue
		}
		source := pushSource
		if def.GetName() == "pull" {
			source = pullSource
		}
		entry := map[string]any{
			"name":                        def.GetName(),
			"source":                      source,
			"realtime":                    def.GetRealtime(),
			"process_controlled_realtime": def.GetProcessControlledRealtime(),
			"stop_sessions":               def.GetStopSessions(),
			"tags":                        def.GetTags(),
		}

		// processing+ and dvr+ sources are resolved dynamically via
		// STREAM_SOURCE. Their base templates exist only to give Mist a
		// configured source for the wildcard families.
		if def.GetName() == "processing" || def.GetName() == "dvr" {
			entry["source"] = inertMistSource
		}
		if def.GetName() == "live" {
			entry["DVR"] = 120000
			entry["resume"] = 1
			entry["inputtimeout"] = 12
		}
		if def.GetName() == "dvr" {
			entry["DVR"] = 120000
			entry["bufferTime"] = 120000
			entry["inputtimeout"] = 12
		}

		// All stream types use STREAM_PROCESS trigger for per-instance process config.
		// No static processes in wildcard definitions.
		streams[def.GetName()] = entry
	}
	return streams
}

func sourceBalancerBase(base, nodeID string) string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return base
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return base
	}
	pathPrefix := strings.TrimRight(u.Path, "/")
	rawPrefix := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = pathPrefix + "/source/by-node/" + nodeID
	u.RawPath = rawPrefix + "/source/by-node/" + url.PathEscape(nodeID)
	return u.String()
}

func staleManagedWildcardStreams(current map[string]any) []string {
	if current == nil {
		return nil
	}
	rawStreams, ok := current["streams"].(map[string]any)
	if !ok {
		return nil
	}
	var stale []string
	for name := range rawStreams {
		if isStaleManagedWildcardStream(name) {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	return stale
}

func missingManagedStreams(current map[string]any, expected map[string]map[string]any) []string {
	if len(expected) == 0 {
		return nil
	}
	rawStreams, ok := current["streams"].(map[string]any)
	if !ok {
		rawStreams = map[string]any{}
	}
	missing := make([]string, 0, len(expected))
	for name := range expected {
		if _, ok := rawStreams[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func isStaleManagedWildcardStream(name string) bool {
	for _, prefix := range []string{"live+", "vod+", "processing+", "pull+", "dvr+"} {
		if strings.HasPrefix(name, prefix) && (name == prefix || strings.Contains(name, "$")) {
			return true
		}
	}
	return false
}

func join(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func hashSeed(seed *ipcpb.ConfigSeed) string {
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
