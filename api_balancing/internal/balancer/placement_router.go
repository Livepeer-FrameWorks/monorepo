package balancer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

const (
	placementRouteTimeout       = 5 * time.Second
	placementDiscoveryTimeout   = 2 * time.Second
	placementPeerTimeout        = time.Second
	placementFanout             = 4
	placementMaxCells           = 64
	placementMaxPrepareAttempts = 8
)

var (
	ErrPlacementUnavailable = errors.New("no permitted placement destination is available")
	ErrPlacementObservation = errors.New("invalid placement observation")
	ErrPlacementPreparation = errors.New("invalid placement preparation acknowledgement")
)

// PlacementCell is resolved from current entitlement and topology authority.
// Multiple virtual clusters in one control cell share one discovery call.
type PlacementCell struct {
	ID         string
	ClusterIDs []string
}

type PlacementRouteRequest struct {
	TenantID               string
	ObjectID               string
	InternalName           string
	SourceGeneration       string
	Verb                   placement.Verb
	Protocol               string
	Policy                 *placement.Policy
	PolicyDigest           string
	PolicyRevision         uint64
	ParentRevision         uint64
	TenantAuthorityVersion int64
	ObjectAuthorityVersion int64
	Location               *placement.Coordinates
	ActiveIngestClusterID  string
	Cells                  []PlacementCell
}

type PlacementCellObservation struct {
	Candidates []placement.Candidate
	Complete   bool
	ObservedAt time.Time
	ExpiresAt  time.Time
}

type PlacementPreparationRequest struct {
	Route     PlacementRouteRequest
	Choice    placement.Choice
	AttemptID string
	ExpiresAt time.Time
}

type PlacementPreparationOutcome string

const (
	PlacementAccepted          PlacementPreparationOutcome = "accepted"
	PlacementFull              PlacementPreparationOutcome = "capacity_exhausted"
	PlacementNodeUnavailable   PlacementPreparationOutcome = "node_unavailable"
	PlacementSourceUnavailable PlacementPreparationOutcome = "source_unavailable"
)

type PlacementPreparationResult struct {
	Outcome          PlacementPreparationOutcome
	TenantID         string
	ObjectID         string
	SourceGeneration string
	ClusterID        string
	NodeID           string
	Protocol         string
	PolicyRevision   uint64
	PolicyDigest     string
	ParentRevision   uint64
	AttemptID        string
	ExpiresAt        time.Time
	// Endpoint is owned by the selected destination: a playback URL for serve,
	// or a credential-free publishing template for ingest.
	Endpoint      string
	PublicBaseURL string
	Ready         bool
}

type PlacementRouteResult struct {
	Decision    placement.Decision
	Preparation PlacementPreparationResult
}

// PlacementRouter shares discovery/evaluation/exact-node preparation between
// front doors. Observe is read-only; Prepare owns final admission revalidation
// and durable idempotency, including reconciliation after an ambiguous reply.
type PlacementRouter struct {
	Observe func(context.Context, PlacementCell, PlacementRouteRequest) (PlacementCellObservation, error)
	Prepare func(context.Context, PlacementCell, PlacementPreparationRequest) (PlacementPreparationResult, error)
	Now     func() time.Time
	// Logger receives per-cell observation failures and refused evaluations;
	// a refusal is otherwise a bare "no permitted destination" to the caller.
	Logger logging.Logger
}

func (router PlacementRouter) logObservationFailures(req PlacementRouteRequest, errs []error, observations []PlacementCellObservation) {
	if router.Logger == nil {
		return
	}
	now := router.now()
	for index, err := range errs {
		fields := logging.Fields{"cell_id": req.Cells[index].ID, "tenant_id": req.TenantID, "internal_name": req.InternalName, "verb": req.Verb}
		switch {
		case err != nil:
			router.Logger.WithError(err).WithFields(fields).Warn("Placement observation failed for cell")
		case !observations[index].Fresh(now):
			router.Logger.WithFields(fields).Warn("Placement observation for cell is not fresh")
		}
	}
}

func (router PlacementRouter) logRefusal(req PlacementRouteRequest, decision placement.Decision, candidates []placement.Candidate, now time.Time) {
	if router.Logger == nil {
		return
	}
	evidence := make(map[string]placement.Candidate, len(candidates))
	for _, candidate := range candidates {
		evidence[placementDestinationKey(candidate.ClusterID, candidate.NodeID)] = candidate
	}
	assessments := make([]string, 0, min(len(decision.Assessments), 20))
	for _, assessment := range decision.Assessments {
		if len(assessments) == cap(assessments) {
			break
		}
		line := assessment.ClusterID + "/" + assessment.NodeID + ":" + assessment.GroupID + ":" + string(assessment.Reason)
		if candidate, ok := evidence[placementDestinationKey(assessment.ClusterID, assessment.NodeID)]; ok {
			// Freshness inputs explain stale_telemetry and unknown_capacity without a debugger.
			line += fmt.Sprintf(" observed=%s expires=%s capacity=%s presence=%s", candidate.ObservedAt.Format(time.RFC3339Nano), candidate.ExpiresAt.Format(time.RFC3339Nano), candidate.Capacity, candidate.Presence)
		}
		assessments = append(assessments, line)
	}
	router.Logger.WithFields(logging.Fields{
		"tenant_id": req.TenantID, "internal_name": req.InternalName, "verb": req.Verb, "reason": decision.Reason, "now": now.Format(time.RFC3339Nano),
		"complete": decision.Complete, "cells": len(req.Cells), "assessed": len(decision.Assessments), "assessments": assessments,
	}).Warn("Placement evaluation found no permitted destination")
}

type placementCensus struct {
	request      PlacementRouteRequest
	observations []PlacementCellObservation
	errors       []error
	candidates   []placement.Candidate
	destinations map[string]PlacementCell
	complete     bool
}

// PlacementEvaluation is a read-only policy decision. Its expiry bounds all
// returned choices and the cross-cell evidence supporting pool transitions.
// It is not a capacity reservation or a first-media attestation.
type PlacementEvaluation struct {
	Decision          placement.Decision
	ExpiresAt         time.Time
	candidateExpiries map[[2]string]time.Time
}

// AssessmentFor returns only an exact candidate with still-current observation
// evidence. An omitted node or expired row cannot certify a preparation refusal.
func (evaluation PlacementEvaluation) AssessmentFor(clusterID, nodeID string, now time.Time) (placement.Assessment, time.Time, bool) {
	expiry, known := evaluation.candidateExpiries[[2]string{clusterID, nodeID}]
	if !known || !now.Before(expiry) {
		return placement.Assessment{}, time.Time{}, false
	}
	for _, assessment := range evaluation.Decision.Assessments {
		if assessment.ClusterID == clusterID && assessment.NodeID == nodeID {
			return assessment, expiry, true
		}
	}
	return placement.Assessment{}, time.Time{}, false
}

func (router PlacementRouter) Evaluate(ctx context.Context, req PlacementRouteRequest) (PlacementEvaluation, error) {
	census, err := router.observePlacement(ctx, req)
	if err != nil {
		return PlacementEvaluation{}, err
	}
	decision, expiresAt, err := census.evaluate(router.now(), census.candidates)
	result := PlacementEvaluation{Decision: decision, ExpiresAt: expiresAt}
	if err != nil {
		return result, err
	}
	result.candidateExpiries = make(map[[2]string]time.Time, len(census.candidates))
	for _, candidate := range census.candidates {
		result.candidateExpiries[[2]string{candidate.ClusterID, candidate.NodeID}] = candidate.ExpiresAt
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return PlacementEvaluation{}, contextErr
	}
	if len(decision.Choices) == 0 {
		router.logRefusal(req, decision, census.candidates, router.now())
		return result, ErrPlacementUnavailable
	}
	choices := make(map[string]bool, len(decision.Choices))
	for _, choice := range decision.Choices {
		choices[placementDestinationKey(choice.ClusterID, choice.NodeID)] = true
	}
	for _, candidate := range census.candidates {
		if choices[placementDestinationKey(candidate.ClusterID, candidate.NodeID)] && candidate.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = candidate.ExpiresAt
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return PlacementEvaluation{}, contextErr
	}
	if !router.now().Before(result.ExpiresAt) {
		return PlacementEvaluation{}, ErrPlacementObservation
	}
	return result, nil
}

func (router PlacementRouter) now() time.Time {
	if router.Now != nil {
		return router.Now()
	}
	return time.Now()
}

func (router PlacementRouter) observePlacement(ctx context.Context, req PlacementRouteRequest) (*placementCensus, error) {
	if err := validatePlacementRoute(req); err != nil {
		return nil, err
	}
	if router.Observe == nil {
		return nil, ErrPlacementUnavailable
	}
	digest, digestErr := placement.Digest(req.Policy)
	if digestErr != nil {
		return nil, digestErr
	}
	if req.PolicyDigest != "" && req.PolicyDigest != digest {
		return nil, ErrPlacementObservation
	}
	req.PolicyDigest = digest
	observations := make([]PlacementCellObservation, len(req.Cells))
	errs := make([]error, len(req.Cells))
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, placementDiscoveryTimeout)
	defer discoveryCancel()
	var group sync.WaitGroup
	work := make(chan int)
	for range min(placementFanout, len(req.Cells)) {
		group.Go(func() {
			for index := range work {
				peerCtx, peerCancel := context.WithTimeout(discoveryCtx, placementPeerTimeout)
				if peerCtx.Err() != nil {
					errs[index] = peerCtx.Err()
				} else {
					observations[index], errs[index] = router.Observe(peerCtx, req.Cells[index], req)
				}
				peerCancel()
			}
		})
	}
enqueue:
	for index := range req.Cells {
		select {
		case work <- index:
		case <-discoveryCtx.Done():
			break enqueue
		}
	}
	close(work)
	group.Wait()
	discoveryCancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	router.logObservationFailures(req, errs, observations)

	candidates := make([]placement.Candidate, 0)
	destinations := make(map[string]PlacementCell)
	complete := true
	for index, observation := range observations {
		if errs[index] != nil || !observation.Fresh(router.now()) {
			complete = false
			continue
		}
		complete = complete && observation.Complete
		cell := req.Cells[index]
		for _, candidate := range observation.Candidates {
			if observation.ExpiresAt.Before(candidate.ExpiresAt) {
				candidate.ExpiresAt = observation.ExpiresAt
			}
			if candidate.TenantID != req.TenantID || candidate.NodeID == "" || !slices.Contains(cell.ClusterIDs, candidate.ClusterID) {
				return nil, ErrPlacementObservation
			}
			key := placementDestinationKey(candidate.ClusterID, candidate.NodeID)
			if _, exists := destinations[key]; exists {
				return nil, ErrPlacementObservation
			}
			destinations[key] = cell
			candidates = append(candidates, candidate)
			if len(candidates) > 4096 {
				return nil, ErrPlacementObservation
			}
		}
	}

	return &placementCensus{request: req, observations: observations, errors: errs, candidates: candidates, destinations: destinations, complete: complete}, nil
}

func (census *placementCensus) evaluate(now time.Time, candidates []placement.Candidate) (placement.Decision, time.Time, error) {
	evaluationComplete := census.complete
	var decisionUntil time.Time
	for index, observation := range census.observations {
		if census.errors[index] != nil || !observation.Fresh(now) {
			evaluationComplete = false
			continue
		}
		if decisionUntil.IsZero() || observation.ExpiresAt.Before(decisionUntil) {
			decisionUntil = observation.ExpiresAt
		}
	}
	req := census.request
	decision, err := placement.Evaluate(placement.Request{
		TenantID: req.TenantID, Verb: req.Verb, Policy: req.Policy, Now: now, Location: req.Location,
		Candidates: candidates, Complete: evaluationComplete, ActiveIngestClusterID: req.ActiveIngestClusterID,
	})
	return decision, decisionUntil, err
}

func (router PlacementRouter) Route(ctx context.Context, req PlacementRouteRequest) (PlacementRouteResult, error) {
	if router.Prepare == nil {
		return PlacementRouteResult{}, ErrPlacementUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, placementRouteTimeout)
	defer cancel()
	census, observationErr := router.observePlacement(ctx, req)
	if observationErr != nil {
		return PlacementRouteResult{}, observationErr
	}
	req = census.request
	candidates, destinations := census.candidates, census.destinations
	now := router.now
	var result PlacementRouteResult
	for range placementMaxPrepareAttempts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		evaluatedAt := now()
		decision, decisionUntil, err := census.evaluate(evaluatedAt, candidates)
		result.Decision = decision
		if err != nil {
			return result, err
		}
		if len(decision.Choices) == 0 {
			router.logRefusal(req, decision, candidates, evaluatedAt)
			return result, ErrPlacementUnavailable
		}
		choice := decision.Choices[0]
		for _, candidate := range candidates {
			if candidate.ClusterID == choice.ClusterID && candidate.NodeID == choice.NodeID && candidate.ExpiresAt.Before(decisionUntil) {
				decisionUntil = candidate.ExpiresAt
			}
		}
		cell := destinations[placementDestinationKey(choice.ClusterID, choice.NodeID)]
		issuedAt := now().Truncate(time.Millisecond)
		if bounded := issuedAt.Add(placement.PreparationLifetime); bounded.Before(decisionUntil) {
			decisionUntil = bounded
		}
		remaining := decisionUntil.Sub(now())
		if remaining <= 0 {
			return result, ErrPlacementObservation
		}
		attemptID, idErr := placement.NewPreparationAttemptID(issuedAt)
		if idErr != nil {
			return result, idErr
		}
		attempt := PlacementPreparationRequest{Route: req, Choice: choice, AttemptID: attemptID, ExpiresAt: decisionUntil}
		prepareCtx, prepareCancel := context.WithTimeout(ctx, remaining)
		prepared, prepareErr := router.Prepare(prepareCtx, cell, attempt)
		prepareContextErr := prepareCtx.Err()
		prepareCancel()
		if prepareErr != nil {
			// An ambiguous outcome is not proof of exhaustion. The destination
			// must reconcile the same attempt; the router cannot invent success.
			return result, prepareErr
		}
		if prepareContextErr != nil {
			return result, prepareContextErr
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !preparationMatches(attempt, prepared, now()) {
			return result, ErrPlacementPreparation
		}
		if prepared.Outcome == PlacementAccepted {
			if prepared.Endpoint == "" || !now().Before(decisionUntil) {
				return result, ErrPlacementPreparation
			}
			result.Preparation = prepared
			return result, nil
		}
		for index := range candidates {
			candidate := &candidates[index]
			if candidate.ClusterID != choice.ClusterID || candidate.NodeID != choice.NodeID {
				continue
			}
			switch prepared.Outcome {
			case PlacementFull:
				candidate.Capacity = placement.CapacityExhausted
			case PlacementNodeUnavailable:
				candidate.Capacity = placement.CapacityUnavailable
			case PlacementSourceUnavailable:
				candidate.Presence, candidate.SourceFeasible = placement.Absent, false
				if req.Verb == placement.Ingest {
					candidate.Capacity = placement.CapacityUnavailable
				}
			default:
				return result, ErrPlacementPreparation
			}
		}
	}
	return result, ErrPlacementUnavailable
}

func (observation PlacementCellObservation) Fresh(now time.Time) bool {
	return !observation.ObservedAt.IsZero() && !observation.ObservedAt.After(now) && now.Before(observation.ExpiresAt) &&
		!observation.ExpiresAt.After(observation.ObservedAt.Add(placementObservationLifetime))
}

func validatePlacementRoute(req PlacementRouteRequest) error {
	if req.TenantID == "" || req.ObjectID == "" || req.InternalName == "" || req.Protocol == "" || len(req.Cells) > placementMaxCells || (req.Verb != placement.Ingest && req.Verb != placement.Serve) {
		return fmt.Errorf("placement route requires bounded tenant, object, verb, protocol and cells")
	}
	if err := placement.Validate(req.Policy); err != nil {
		return err
	}
	cells, clusters := map[string]bool{}, map[string]bool{}
	for _, cell := range req.Cells {
		if cell.ID == "" || cells[cell.ID] || len(cell.ClusterIDs) == 0 || len(cell.ClusterIDs) > 4096 {
			return ErrPlacementObservation
		}
		cells[cell.ID] = true
		for _, cluster := range cell.ClusterIDs {
			if cluster == "" || clusters[cluster] {
				return ErrPlacementObservation
			}
			clusters[cluster] = true
		}
	}
	return nil
}

func placementDestinationKey(cluster, node string) string {
	return fmt.Sprintf("%d:%s%s", len(cluster), cluster, node)
}

func preparationMatches(req PlacementPreparationRequest, result PlacementPreparationResult, now time.Time) bool {
	return result.TenantID == req.Route.TenantID && result.ObjectID == req.Route.ObjectID && result.SourceGeneration == req.Route.SourceGeneration &&
		result.ClusterID == req.Choice.ClusterID && result.NodeID == req.Choice.NodeID && result.Protocol == req.Route.Protocol &&
		result.PolicyRevision == req.Route.PolicyRevision && result.ParentRevision == req.Route.ParentRevision && result.AttemptID == req.AttemptID &&
		result.PolicyDigest == req.Route.PolicyDigest &&
		result.ExpiresAt.After(now) && !result.ExpiresAt.After(req.ExpiresAt) && !result.ExpiresAt.After(now.Add(time.Minute))
}
