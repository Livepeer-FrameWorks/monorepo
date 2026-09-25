package handlers

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"

	"github.com/gin-gonic/gin"
)

// livepeerAuthRequest is the body sent by go-livepeer's auth webhook.
type livepeerAuthRequest struct {
	URL               string                `json:"url"`
	Profiles          []livepeerJSONProfile `json:"profiles,omitempty"`
	ContentResolution string                `json:"contentResolution,omitempty"`
	Source            livepeerSource        `json:"source"`
	JobToken          string                `json:"jobToken"`
	RemoteIP          string                `json:"remoteIP"`
}

type livepeerSource struct {
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	FPS         float64 `json:"fps"`
	Codec       string  `json:"codec"`
	PixelFormat string  `json:"pixelFormat"`
}

// livepeerAuthResponse is what go-livepeer expects back.
// ManifestID is required — an empty value or non-200 status rejects the stream.
// TenantID and StreamID propagate FrameWorks tenant context into go-livepeer's
// authWebhookResponse → core.StreamParameters, so the gateway can stamp
// per-session telemetry with the right tenant. Every response field is
// mandatory in the coordinated Platform 1 contract.
type livepeerAuthResponse struct {
	ManifestID           string                `json:"manifestID"`
	TenantID             string                `json:"tenantID"`
	StreamID             string                `json:"streamID"`
	Profiles             []livepeerJSONProfile `json:"profiles"`
	Workload             string                `json:"workload"`
	DeadlineMs           int                   `json:"deadlineMs"`
	MinSpeed             float64               `json:"minSpeed"`
	AuthorizedEdgeNodeID string                `json:"authorizedEdgeNodeID"`
	SpecDigest           string                `json:"specDigest"`
}

// LivepeerAuthContext is the resolved tenant/stream context for an authorized
// livepeer-gateway transcode request. Authorize returns this on success and
// nil on rejection. The fields here flow into the auth webhook response and,
// from there, into go-livepeer's StreamParameters via createRTMPStreamIDHandler.
type LivepeerAuthContext struct {
	TenantID       string
	StreamID       string
	InternalName   string
	ProcessesJSON  string
	Profiles       []livepeerJSONProfile
	Workload       string
	DeadlineMs     int
	MinSpeed       float64
	NodeID         string
	SpecDigest     string
	ExpectedSource *livepeerSource
}

type livepeerJSONProfile = mist.LivepeerJSONProfile

// HandleLivepeerAuth handles the auth webhook from go-livepeer gateways.
// It validates that the manifestID in the push URL corresponds to an active
// stream owned by a real tenant — refuses random unauthorised transcode requests.
//
// URL format: http://gateway:8935/live/<manifestID>/<segNum>.ts
func HandleLivepeerAuth(c *gin.Context) {
	var req livepeerAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.WithError(err).Warn("livepeer auth: invalid request body")
		incLivepeerAuthRejected(authRejectInvalidRequest)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	manifestID := extractManifestID(req.URL)
	if manifestID == "" {
		logger.WithField("url", req.URL).Warn("livepeer auth: could not extract manifestID from URL")
		incLivepeerAuthRejected(authRejectInvalidRequest)
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid stream URL"})
		return
	}

	authCtx, reason := authorizeSignedLivepeerJob(c.Request.Context(), manifestID, req)
	if authCtx == nil {
		logger.WithFields(logging.Fields{
			"manifest_id": manifestID,
			"reason":      reason,
		}).Warn("livepeer auth: unknown stream rejected")
		incLivepeerAuthRejected(reason)
		c.JSON(http.StatusForbidden, gin.H{"error": "unknown stream"})
		return
	}

	logger.WithFields(logging.Fields{
		"manifest_id": manifestID,
		"tenant_id":   authCtx.TenantID,
		"stream_id":   authCtx.StreamID,
	}).Debug("livepeer auth: stream authorized")
	c.JSON(http.StatusOK, livepeerAuthResponse{
		ManifestID:           manifestID,
		TenantID:             authCtx.TenantID,
		StreamID:             authCtx.StreamID,
		Profiles:             authCtx.Profiles,
		Workload:             authCtx.Workload,
		DeadlineMs:           authCtx.DeadlineMs,
		MinSpeed:             authCtx.MinSpeed,
		AuthorizedEdgeNodeID: authCtx.NodeID,
		SpecDigest:           authCtx.SpecDigest,
	})
}

func authorizeSignedLivepeerJob(ctx context.Context, manifestID string, req livepeerAuthRequest) (*LivepeerAuthContext, string) {
	// Verify the capability before any resolver/database call so the public
	// webhook cannot be used to probe tenant or stream existence.
	claims, reason := verifyLivepeerJobToken(manifestID, req.JobToken, time.Now())
	if reason != "" {
		return nil, reason
	}
	remoteIP := strings.TrimSpace(req.RemoteIP)
	nodeID := state.DefaultManager().NodeIDByClientIP(remoteIP)
	if nodeID == "" || nodeID != claims.NodeID {
		return nil, authRejectNodeMismatch
	}
	node := state.DefaultManager().GetNodeState(nodeID)
	if node == nil || !node.IsHealthy || node.IsStale || node.ClusterID != claims.ClusterID || !node.CapEdge {
		return nil, authRejectNodeUnhealthy
	}

	var authCtx *LivepeerAuthContext
	if isProcessingManifestID(claims.ManifestID) {
		if !node.CapProcessing {
			return nil, authRejectNodeCapability
		}
		if _, ok := node.ClassLoad(mist.ProcessingClassVideoTranscode); !ok {
			return nil, authRejectNodeCapability
		}
		authCtx = authorizeProcessingTranscode(ctx, claims)
	} else {
		if !node.CapIngest {
			return nil, authRejectNodeCapability
		}
		authCtx = authorizeLiveTranscode(ctx, claims)
	}
	if authCtx == nil {
		return nil, authRejectStaleJob
	}

	if authCtx.ExpectedSource != nil && !livepeerSourceMatches(req.Source, *authCtx.ExpectedSource) {
		if logger != nil {
			logger.WithFields(logging.Fields{
				"manifest_id":     manifestID,
				"observed_source": req.Source,
				"expected_source": *authCtx.ExpectedSource,
			}).Warn("livepeer auth: gateway source differs from the authorised job source")
		}
		return nil, authRejectSourceMismatch
	}
	source := mist.SourceMediaInfo{Width: req.Source.Width, Height: req.Source.Height, FPS: req.Source.FPS}
	spec, err := mist.LivepeerJobSpecFromProcessesJSON(authCtx.ProcessesJSON)
	if err != nil {
		return nil, authRejectInvalidSpec
	}
	profiles := mist.NormalizeLivepeerProfiles(spec.Profiles, source)
	if len(profiles) == 0 {
		return nil, authRejectInvalidSpec
	}
	if len(req.Profiles) > 0 {
		observed := mist.NormalizeLivepeerProfiles(req.Profiles, source)
		if diff := mist.LivepeerProfileMismatch(observed, profiles, source); diff != "" {
			if logger != nil {
				logger.WithFields(logging.Fields{
					"manifest_id":       manifestID,
					"difference":        diff,
					"observed_profiles": observed,
					"expected_profiles": profiles,
					"source":            req.Source,
				}).Warn("livepeer auth: gateway profiles differ from the authorised job spec")
			}
			return nil, authRejectSpecMismatch
		}
		if mist.LivepeerProfileMismatch(req.Profiles, profiles, source) != "" && metrics != nil && metrics.LivepeerAuthProfileNormalized != nil {
			metrics.LivepeerAuthProfileNormalized.WithLabelValues().Inc()
		}
	}
	authCtx.InternalName = canonicalLivepeerManifestID(manifestID)
	authCtx.ProcessesJSON = ""
	authCtx.Profiles = profiles
	authCtx.Workload = spec.Workload
	authCtx.DeadlineMs = spec.DeadlineMs
	authCtx.MinSpeed = spec.MinSpeed
	authCtx.NodeID = claims.NodeID
	authCtx.SpecDigest = claims.SpecDigest
	return authCtx, ""
}

// verifyLivepeerJobToken checks the job token's signature and claims, then binds
// it to the requested manifest and to this Foghorn's cluster as an allowed
// gateway cell. A rejection returns an invalid_token_<check> reason and logs the
// failed check with the identities involved, so a cross-cell or secret-drift
// rejection names its cause.
func verifyLivepeerJobToken(manifestID, token string, now time.Time) (control.TranscodeJobClaims, string) {
	claims, err := control.VerifyTranscodeJobTokenWithConfiguredSecret(token, now)
	fields := logging.Fields{
		"manifest_id":      manifestID,
		"local_cluster_id": clusterID,
	}
	if err != nil {
		check := control.TranscodeJobTokenCheck(err)
		if check == "" {
			check = control.TranscodeTokenCheckFormat
		}
		logger.WithError(err).WithFields(fields).WithField("check", check).Warn("livepeer auth: job token verification failed")
		return control.TranscodeJobClaims{}, authRejectInvalidToken + "_" + check
	}
	fields["token_manifest_id"] = claims.ManifestID
	fields["token_cluster_id"] = claims.ClusterID
	fields["token_node_id"] = claims.NodeID
	fields["token_job_id"] = claims.JobID
	fields["allowed_gateway_cluster_ids"] = claims.AllowedGatewayClusterIDs
	fields["issued_at"] = claims.IssuedAt
	if canonicalLivepeerManifestID(manifestID) != claims.ManifestID {
		logger.WithFields(fields).WithField("check", tokenCheckManifestMismatch).Warn("livepeer auth: job token is bound to another manifest")
		return control.TranscodeJobClaims{}, authRejectInvalidToken + "_" + tokenCheckManifestMismatch
	}
	if !livepeerGatewayCellAllowed(claims, clusterID) {
		logger.WithFields(fields).WithField("check", tokenCheckGatewayCluster).Warn("livepeer auth: job token does not allow this Foghorn's cluster as the gateway cell")
		return control.TranscodeJobClaims{}, authRejectInvalidToken + "_" + tokenCheckGatewayCluster
	}
	return claims, ""
}

func authorizeProcessingTranscode(ctx context.Context, claims control.TranscodeJobClaims) *LivepeerAuthContext {
	if db == nil || claims.JobID == "" || claims.Session != claims.JobID {
		return nil
	}
	artifactHash := mist.ExtractInternalName(claims.ManifestID)
	row, err := foghorndb.New(db).GetProcessingJobAuthContext(ctx, foghorndb.GetProcessingJobAuthContextParams{
		JobID: claims.JobID, ArtifactHash: artifactHash,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return authorizeChapterTranscode(ctx, artifactHash, claims)
	}
	if err != nil || row.JobID != claims.JobID || row.TenantID != claims.TenantID ||
		row.ProcessingNodeID != claims.NodeID || strconv.Itoa(int(row.RetryCount.Int32)) != claims.AttemptOrGeneration ||
		!row.ProcessesJson.Valid {
		return nil
	}
	digest, err := mist.LivepeerJobSpecDigest(row.ProcessesJson.String)
	if err != nil || digest != claims.SpecDigest {
		return nil
	}
	streamID := strings.TrimSpace(row.StreamID)
	if streamID == "" {
		streamID = "job:" + row.JobID
	}
	expectedSource := &livepeerSource{Codec: row.InputCodec}
	if row.Width.Valid {
		expectedSource.Width = int(row.Width.Int32)
	}
	if row.Height.Valid {
		expectedSource.Height = int(row.Height.Int32)
	}
	if row.Fps.Valid {
		expectedSource.FPS = row.Fps.Float64
	}
	return &LivepeerAuthContext{TenantID: row.TenantID, StreamID: streamID, ProcessesJSON: row.ProcessesJson.String, ExpectedSource: expectedSource}
}

func authorizeChapterTranscode(ctx context.Context, artifactHash string, claims control.TranscodeJobClaims) *LivepeerAuthContext {
	row, err := foghorndb.New(db).ActiveChapterTranscodeJobContext(ctx, artifactHash)
	if err != nil || row.TenantID != claims.TenantID || row.ProcessingNodeID != claims.NodeID ||
		strconv.Itoa(int(row.FinalizeAttempts)) != claims.AttemptOrGeneration ||
		foghorndb.ChapterFinalizeJobID(row.FinalizeAttempts, row.ChapterID) != claims.JobID {
		return nil
	}
	digest, err := mist.LivepeerJobSpecDigest(row.ProcessesJson)
	if err != nil || digest != claims.SpecDigest {
		return nil
	}
	streamID := strings.TrimSpace(row.StreamID)
	if streamID == "" {
		streamID = "job:" + claims.JobID
	}
	return &LivepeerAuthContext{TenantID: row.TenantID, StreamID: streamID, ProcessesJSON: row.ProcessesJson}
}

func authorizeLiveTranscode(ctx context.Context, claims control.TranscodeJobClaims) *LivepeerAuthContext {
	if claims.Session == "" || claims.Session != claims.AttemptOrGeneration {
		return rejectLiveTranscode(claims, liveCheckGeneration, nil)
	}
	internalName := mist.ExtractInternalName(claims.ManifestID)
	if strings.HasPrefix(claims.Session, "state:") {
		stream := state.DefaultManager().GetStreamState(internalName)
		if stream == nil {
			return rejectLiveTranscode(claims, liveCheckStreamState, logging.Fields{"stream_status": "absent"})
		}
		if stream.Status != "live" || stream.NodeID != claims.NodeID || stream.TenantID != claims.TenantID ||
			stream.LivepeerGeneration != claims.Session || stream.LivepeerSpecDigest != claims.SpecDigest || stream.LivepeerProcessesJSON == "" || stream.StreamID == "" {
			return rejectLiveTranscode(claims, liveCheckStreamState, logging.Fields{
				"stream_status":      stream.Status,
				"stream_node_id":     stream.NodeID,
				"stream_generation":  stream.LivepeerGeneration,
				"stream_spec_digest": stream.LivepeerSpecDigest,
			})
		}
		return &LivepeerAuthContext{TenantID: stream.TenantID, StreamID: stream.StreamID, ProcessesJSON: stream.LivepeerProcessesJSON}
	}
	if db == nil {
		return rejectLiveTranscode(claims, liveCheckSessionLookup, logging.Fields{"error": "no database"})
	}
	row, err := foghorndb.New(db).GetLiveTranscodeAuthContext(ctx, foghorndb.GetLiveTranscodeAuthContextParams{
		SessionID: claims.Session, StreamInternalName: internalName,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// The token's ingest session has ended or is not active for this stream,
		// e.g. a buffer that outlived its publisher still holds the previous
		// session's signed process config.
		return rejectLiveTranscode(claims, liveCheckSessionNotActive, nil)
	}
	if err != nil {
		return rejectLiveTranscode(claims, liveCheckSessionLookup, logging.Fields{"error": err.Error()})
	}
	if row.SessionID != claims.Session || row.TenantID != claims.TenantID ||
		row.NodeID != claims.NodeID || row.IngestClusterID != claims.ClusterID {
		return rejectLiveTranscode(claims, liveCheckSessionBinding, logging.Fields{
			"session_node_id":    row.NodeID,
			"session_cluster_id": row.IngestClusterID,
		})
	}
	digest, err := mist.LivepeerJobSpecDigest(row.ProcessesJson)
	if err != nil || digest != claims.SpecDigest {
		return rejectLiveTranscode(claims, liveCheckSpecDigest, logging.Fields{"session_spec_digest": digest})
	}
	streamID := ""
	if stream := state.DefaultManager().GetStreamState(internalName); stream != nil {
		streamID = strings.TrimSpace(stream.StreamID)
	}
	if streamID == "" {
		return rejectLiveTranscode(claims, liveCheckStreamState, logging.Fields{"stream_status": "no stream id"})
	}
	return &LivepeerAuthContext{TenantID: row.TenantID, StreamID: streamID, ProcessesJSON: row.ProcessesJson}
}

// rejectLiveTranscode logs which live binding a stale_job rejection failed, with
// the token's identities and the observed values, and returns nil.
func rejectLiveTranscode(claims control.TranscodeJobClaims, check string, observed logging.Fields) *LivepeerAuthContext {
	if logger == nil {
		return nil
	}
	fields := logging.Fields{
		"check":             check,
		"token_manifest_id": claims.ManifestID,
		"token_session":     claims.Session,
		"token_generation":  claims.AttemptOrGeneration,
		"token_node_id":     claims.NodeID,
		"token_cluster_id":  claims.ClusterID,
		"token_spec_digest": claims.SpecDigest,
		"issued_at":         claims.IssuedAt,
	}
	for k, v := range observed {
		fields[k] = v
	}
	logger.WithFields(fields).Warn("livepeer auth: live job token is not bound to the stream's current session")
	return nil
}

func livepeerSourceMatches(observed, expected livepeerSource) bool {
	if expected.Width > 0 && observed.Width != expected.Width {
		return false
	}
	if expected.Height > 0 && observed.Height != expected.Height {
		return false
	}
	if expected.FPS > 0 && math.Abs(observed.FPS-expected.FPS) > 0.1 {
		return false
	}
	canonicalCodec := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		return strings.NewReplacer(".", "", "-", "", "_", "").Replace(value)
	}
	return expected.Codec == "" || canonicalCodec(observed.Codec) == canonicalCodec(expected.Codec)
}

// livepeerGatewayCellAllowed reports whether this Foghorn may answer the job's
// auth webhook. The token names the media cluster whose gateway was selected;
// Quartermaster discovery reports a pool-assigned gateway under that assigned
// cluster, which for a virtual cluster is served by this cell's Foghorn rather
// than being its own CLUSTER_ID.
func livepeerGatewayCellAllowed(claims control.TranscodeJobClaims, localClusterID string) bool {
	if control.TranscodeJobTokenAllowsGatewayCluster(claims, localClusterID) {
		return true
	}
	for _, id := range claims.AllowedGatewayClusterIDs {
		if servesLivepeerGatewayCluster(id) {
			return true
		}
	}
	return false
}

// servesLivepeerGatewayCluster is control.IsServedCluster; tests replace it.
var servesLivepeerGatewayCluster = control.IsServedCluster

func canonicalLivepeerManifestID(manifestID string) string {
	manifestID = strings.TrimSpace(manifestID)
	if dash := strings.LastIndexByte(manifestID, '-'); dash > 0 && len(manifestID)-dash-1 == 8 {
		suffix := manifestID[dash+1:]
		valid := true
		for _, r := range suffix {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				valid = false
				break
			}
		}
		if valid {
			return manifestID[:dash]
		}
	}
	return manifestID
}

// extractManifestID parses the manifestID from a go-livepeer push URL.
// Expected path: /live/<manifestID>/<segNum>.ts (or just /live/<manifestID>/...)
// The segment is decoded with the shared stream-name codec: the gateway
// forwards the push target with one of MistServer's encoding layers still in
// place, so a single url.Parse decode leaves "live%2b..." instead of "live+...".
func extractManifestID(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Path: /live/<manifestID>/0.ts
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "live" {
		return ""
	}
	manifestID, err := mist.DecodeStreamNamePath(parts[1])
	if err != nil {
		return ""
	}
	return manifestID
}

// LivepeerAuthRejection reasons reported via metrics + structured log.
const (
	authRejectInvalidRequest = "invalid_request"
	// authRejectInvalidToken prefixes invalid_token_<check>, where check is a
	// control.TranscodeTokenCheck* value or one of the binding checks below.
	authRejectInvalidToken   = "invalid_token"
	authRejectNodeMismatch   = "node_mismatch"
	authRejectNodeUnhealthy  = "node_unhealthy"
	authRejectNodeCapability = "node_capability"
	authRejectStaleJob       = "stale_job"
	authRejectInvalidSpec    = "invalid_spec"
	authRejectSpecMismatch   = "spec_mismatch"
	authRejectSourceMismatch = "source_mismatch"

	// Token binding checks performed after signature and claim verification.
	tokenCheckManifestMismatch = "manifest_mismatch"
	tokenCheckGatewayCluster   = "gateway_cluster"

	// Live stale_job checks, logged by rejectLiveTranscode.
	liveCheckGeneration       = "generation"
	liveCheckStreamState      = "stream_state"
	liveCheckSessionNotActive = "session_not_active"
	liveCheckSessionLookup    = "session_lookup"
	liveCheckSessionBinding   = "session_binding"
	liveCheckSpecDigest       = "spec_digest"
)

func incLivepeerAuthRejected(reason string) {
	if metrics == nil || metrics.LivepeerAuthRejected == nil {
		return
	}
	metrics.LivepeerAuthRejected.WithLabelValues(reason).Inc()
}

func isProcessingManifestID(manifestID string) bool {
	return strings.HasPrefix(manifestID, "processing+")
}
