package control

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type restreamTarget struct {
	targetID          string
	tenantID          string
	streamID          string
	streamName        string
	sourceGeneration  string
	targetRevision    int64
	activationAttempt string
	platform          string
	targetURI         string
}

type restreamDesiredState struct {
	sourceGeneration string
	targetRevision   int64
	dispatchSequence uint64
	byID             map[string]restreamTarget
	uriToID          map[string]string
	pushIDToID       map[int64]string
	retiredByURI     map[string]retiredRestreamTarget
	retiredByPushID  map[int64]retiredRestreamTarget
}

type retiredRestreamTarget struct {
	restreamTarget
	expiresAt time.Time
}

type restreamInstallResult uint8

const (
	restreamInstallInvalid restreamInstallResult = iota
	restreamInstallApplied
	restreamInstallSuperseded
)

const restreamRetiredIdentityTTL = 15 * time.Minute

var restreamRegistry = struct {
	sync.RWMutex
	streams map[string]restreamDesiredState
}{streams: make(map[string]restreamDesiredState)}

func pruneRestreamRegistryLocked(now time.Time) {
	for streamName, state := range restreamRegistry.streams {
		for uri, retired := range state.retiredByURI {
			if !retired.expiresAt.After(now) {
				delete(state.retiredByURI, uri)
			}
		}
		for pushID, retired := range state.retiredByPushID {
			if !retired.expiresAt.After(now) {
				delete(state.retiredByPushID, pushID)
			}
		}
		if len(state.byID) == 0 && len(state.retiredByURI) == 0 && len(state.retiredByPushID) == 0 {
			delete(restreamRegistry.streams, streamName)
			continue
		}
		restreamRegistry.streams[streamName] = state
	}
}

func installRestreamDesiredState(req *ipcpb.ActivatePushTargets) (restreamDesiredState, restreamInstallResult) {
	return installRestreamDesiredStateForControlEpoch(req, "", restreamActivationDispatchSequence.Add(1))
}

func installRestreamDesiredStateForControlEpoch(req *ipcpb.ActivatePushTargets, controlEpoch string, dispatchSequence uint64) (restreamDesiredState, restreamInstallResult) {
	if req == nil {
		return restreamDesiredState{}, restreamInstallInvalid
	}
	next := restreamDesiredState{
		sourceGeneration: strings.TrimSpace(req.GetSourceGeneration()),
		targetRevision:   req.GetTargetRevision(),
		dispatchSequence: dispatchSequence,
		byID:             make(map[string]restreamTarget, len(req.GetTargets())),
		uriToID:          make(map[string]string, len(req.GetTargets())),
		pushIDToID:       make(map[int64]string, len(req.GetTargets())),
		retiredByURI:     make(map[string]retiredRestreamTarget),
		retiredByPushID:  make(map[int64]retiredRestreamTarget),
	}
	for _, spec := range req.GetTargets() {
		if spec == nil || strings.TrimSpace(spec.GetTargetId()) == "" || strings.TrimSpace(spec.GetTargetUri()) == "" {
			return restreamDesiredState{}, restreamInstallInvalid
		}
		targetID := strings.TrimSpace(spec.GetTargetId())
		uri := strings.TrimSpace(spec.GetTargetUri())
		if _, duplicate := next.byID[targetID]; duplicate {
			return restreamDesiredState{}, restreamInstallInvalid
		}
		if _, duplicate := next.uriToID[uri]; duplicate {
			return restreamDesiredState{}, restreamInstallInvalid
		}
		next.byID[targetID] = restreamTarget{
			targetID: targetID, tenantID: strings.TrimSpace(req.GetTenantId()),
			streamID: strings.TrimSpace(req.GetStreamId()), streamName: strings.TrimSpace(req.GetStreamName()),
			sourceGeneration: next.sourceGeneration, targetRevision: next.targetRevision,
			activationAttempt: strings.TrimSpace(req.GetActivationAttempt()),
			platform:          strings.TrimSpace(spec.GetPlatform()), targetURI: uri,
		}
		next.uriToID[uri] = targetID
	}
	restreamRegistry.Lock()
	defer restreamRegistry.Unlock()
	if controlEpoch != "" {
		active := getConnection()
		if active == nil || active.epoch != controlEpoch {
			return restreamDesiredState{}, restreamInstallSuperseded
		}
	}
	pruneRestreamRegistryLocked(time.Now())
	current, exists := restreamRegistry.streams[req.GetStreamName()]
	if exists && current.sourceGeneration == next.sourceGeneration && next.targetRevision < current.targetRevision {
		return current, restreamInstallSuperseded
	}
	if exists && current.sourceGeneration == next.sourceGeneration && next.targetRevision == current.targetRevision &&
		next.dispatchSequence <= current.dispatchSequence {
		return current, restreamInstallSuperseded
	}
	if exists {
		now := time.Now()
		for uri, retired := range current.retiredByURI {
			if retired.expiresAt.After(now) {
				next.retiredByURI[uri] = retired
			}
		}
		for pushID, retired := range current.retiredByPushID {
			if retired.expiresAt.After(now) {
				next.retiredByPushID[pushID] = retired
			}
		}
		for targetID, target := range current.byID {
			nextTarget, stillDesired := next.byID[targetID]
			if current.sourceGeneration != next.sourceGeneration || !stillDesired || nextTarget.targetURI != target.targetURI ||
				nextTarget.activationAttempt != target.activationAttempt {
				next.retiredByURI[target.targetURI] = retiredRestreamTarget{
					restreamTarget: target,
					expiresAt:      now.Add(restreamRetiredIdentityTTL),
				}
			}
		}
		for pushID, targetID := range current.pushIDToID {
			target, ok := current.byID[targetID]
			if !ok {
				continue
			}
			nextTarget, unchanged := next.byID[targetID]
			unchanged = unchanged && current.sourceGeneration == next.sourceGeneration &&
				nextTarget.targetURI == target.targetURI && nextTarget.activationAttempt == target.activationAttempt
			if unchanged {
				next.pushIDToID[pushID] = targetID
				continue
			}
			next.retiredByPushID[pushID] = retiredRestreamTarget{
				restreamTarget: target,
				expiresAt:      now.Add(restreamRetiredIdentityTTL),
			}
		}
	}
	restreamRegistry.streams[req.GetStreamName()] = next
	return next, restreamInstallApplied
}

func clearRestreamDesiredState(streamName, sourceGeneration string) {
	restreamRegistry.Lock()
	defer restreamRegistry.Unlock()
	current, ok := restreamRegistry.streams[streamName]
	if ok && (sourceGeneration == "" || current.sourceGeneration == sourceGeneration) {
		now := time.Now()
		if current.retiredByURI == nil {
			current.retiredByURI = make(map[string]retiredRestreamTarget)
		}
		if current.retiredByPushID == nil {
			current.retiredByPushID = make(map[int64]retiredRestreamTarget)
		}
		for _, target := range current.byID {
			current.retiredByURI[target.targetURI] = retiredRestreamTarget{
				restreamTarget: target,
				expiresAt:      now.Add(restreamRetiredIdentityTTL),
			}
		}
		for pushID, targetID := range current.pushIDToID {
			if target, exists := current.byID[targetID]; exists {
				current.retiredByPushID[pushID] = retiredRestreamTarget{
					restreamTarget: target,
					expiresAt:      now.Add(restreamRetiredIdentityTTL),
				}
			}
		}
		current.byID = make(map[string]restreamTarget)
		current.uriToID = make(map[string]string)
		current.pushIDToID = make(map[int64]string)
		restreamRegistry.streams[streamName] = current
	}
}

func restreamTargetReports(streamName, sourceGeneration string) []*ipcpb.PushTargetStatusReport {
	restreamRegistry.Lock()
	defer restreamRegistry.Unlock()
	pruneRestreamRegistryLocked(time.Now())
	state, ok := restreamRegistry.streams[streamName]
	if !ok || state.sourceGeneration != strings.TrimSpace(sourceGeneration) {
		return nil
	}
	reports := make([]*ipcpb.PushTargetStatusReport, 0, len(state.byID))
	for _, target := range state.byID {
		reports = append(reports, restreamReport(target))
	}
	return reports
}

func restreamReport(target restreamTarget) *ipcpb.PushTargetStatusReport {
	return &ipcpb.PushTargetStatusReport{
		TargetId: target.targetID, TenantId: target.tenantID, StreamId: target.streamID,
		StreamName: target.streamName, SourceGeneration: target.sourceGeneration,
		TargetRevision: target.targetRevision, Platform: target.platform,
		ActivationAttempt: target.activationAttempt,
	}
}

// bindRestreamPushIDs records Mist's runtime process identity after inventory
// convergence. PUSH_END includes this ID, so a delayed final cannot be rebound
// to a replacement generation that happens to reuse the same destination URI.
func bindRestreamPushIDs(streamName, sourceGeneration string, targetRevision int64, pushes []mist.PushInfo) {
	restreamRegistry.Lock()
	defer restreamRegistry.Unlock()
	state, ok := restreamRegistry.streams[streamName]
	if !ok || state.sourceGeneration != strings.TrimSpace(sourceGeneration) || state.targetRevision != targetRevision {
		return
	}
	if state.pushIDToID == nil {
		state.pushIDToID = make(map[int64]string)
	}
	for _, push := range pushes {
		if push.StreamName != streamName {
			continue
		}
		if targetID, found := state.uriToID[strings.TrimSpace(push.TargetURI)]; found {
			state.pushIDToID[int64(push.ID)] = targetID
			delete(state.retiredByPushID, int64(push.ID))
		}
	}
	restreamRegistry.streams[streamName] = state
}

// ResolveRestreamTarget maps a secret-bearing Mist identity to server-issued
// target identity entirely in memory. The URI never enters the returned report.
func ResolveRestreamTarget(streamName string, pushID int64, primaryURI, alternateURI string) (*ipcpb.PushTargetStatusReport, bool) {
	restreamRegistry.Lock()
	defer restreamRegistry.Unlock()
	pruneRestreamRegistryLocked(time.Now())
	state, ok := restreamRegistry.streams[streamName]
	if !ok {
		return nil, false
	}
	if pushID != 0 {
		if targetID, found := state.pushIDToID[pushID]; found {
			if target, current := state.byID[targetID]; current {
				return restreamReport(target), true
			}
		}
		if retired, found := state.retiredByPushID[pushID]; found && retired.expiresAt.After(time.Now()) {
			return restreamReport(retired.restreamTarget), true
		}
		// PUSH_END carries Mist's process identity. Never fall back to URI for
		// an unknown non-zero ID: a replacement generation may intentionally
		// reuse the same destination and must not inherit the delayed final.
		return nil, false
	}
	targetID, found := state.uriToID[strings.TrimSpace(primaryURI)]
	if !found {
		targetID = state.uriToID[strings.TrimSpace(alternateURI)]
	}
	target, found := state.byID[targetID]
	if !found {
		if retired, ok := state.retiredByURI[strings.TrimSpace(primaryURI)]; ok && retired.expiresAt.After(time.Now()) {
			target, found = retired.restreamTarget, true
		}
	}
	if !found {
		if retired, ok := state.retiredByURI[strings.TrimSpace(alternateURI)]; ok && retired.expiresAt.After(time.Now()) {
			target, found = retired.restreamTarget, true
		}
	}
	if !found {
		return nil, false
	}
	return restreamReport(target), true
}

// SendRestreamStatus sends a non-final sanitized lifecycle transition. Final
// reports use the durable trigger WAL in the HTTP handler.
func SendRestreamStatus(ctx context.Context, report *ipcpb.PushTargetStatusReport, logger logging.Logger) error {
	if report == nil {
		return nil
	}
	tenantID := strings.TrimSpace(report.GetTenantId())
	trigger := &ipcpb.MistTrigger{
		TriggerType: string(mist.TriggerRestreamStatus), NodeId: GetCurrentNodeID(), Timestamp: time.Now().UnixMilli(),
		TenantId: &tenantID, TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: report},
	}
	_, err := SendMistTriggerContext(context.WithoutCancel(ctx), trigger, logger)
	return err
}
