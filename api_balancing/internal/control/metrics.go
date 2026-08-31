package control

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/prometheus/client_golang/prometheus"
)

// ControlMetrics holds Prometheus metrics for the HelmsmanControl stream ingress.
type ControlMetrics struct {
	// MistTriggers counts MistTrigger messages received/processed over the HelmsmanControl stream.
	// Labels: trigger_type, blocking ("true"|"false"), status
	MistTriggers *prometheus.CounterVec
	// RelayForwards counts cross-instance relay attempts and outcomes.
	// Labels: command_type, status
	RelayForwards *prometheus.CounterVec
	// ArtifactSyncOutcomes counts SyncComplete outcomes reported by Helmsman.
	// Labels: outcome ("success"|"failed"|"lost_local"|"dtsh_failed").
	// outcome="lost_local" is terminal data loss (the local source was gone
	// before the S3 sync succeeded and is never retried) — alert on its rate.
	// outcome="dtsh_failed" is a retryable incremental .dtsh index sync failure.
	ArtifactSyncOutcomes *prometheus.CounterVec
	// ArtifactDeletionOutcomes counts point-deletion database decisions.
	// Labels: outcome (applied|fenced|absent|parent_missing|error).
	ArtifactDeletionOutcomes *prometheus.CounterVec
	// MediaRequestCentralRPCs counts logical Commodore, Quartermaster, and
	// Purser client invocations made beneath a media request. Retries inside the
	// failsafe interceptor are intentionally not counted. Labels are bounded
	// path/service/method enums; object and tenant identifiers are excluded.
	MediaRequestCentralRPCs *prometheus.CounterVec
	// NodeAdmissionEvents records durable outage-identity operations.
	// Labels: operation (load|persist|revoke|preauth|proof_prune), result
	// (success|failure|saturated).
	NodeAdmissionEvents *prometheus.CounterVec
	// AdmissionPayloadCrypto records the bounded legacy/v2 open and migration
	// outcomes for durable push-target activation payloads.
	// Labels: format (plaintext|v1|v2), result (opened|migrated|error).
	AdmissionPayloadCrypto *prometheus.CounterVec
}

// MediaRequestContext tags a bounded request-path name. The shared service
// client interceptors invoke its observer once per logical central RPC.
func MediaRequestContext(ctx context.Context, path string) context.Context {
	return ctxkeys.WithMediaRequestRPCObserver(ctx, path, func(path, service, method string) {
		if controlMetrics == nil || controlMetrics.MediaRequestCentralRPCs == nil {
			return
		}
		controlMetrics.MediaRequestCentralRPCs.WithLabelValues(path, service, method).Inc()
	})
}

var controlMetrics *ControlMetrics

// SetMetrics configures optional Prometheus metrics for the control server.
func SetMetrics(m *ControlMetrics) {
	controlMetrics = m
}

func incMistTrigger(triggerType string, blocking bool, status string) {
	if controlMetrics == nil || controlMetrics.MistTriggers == nil {
		return
	}
	b := "false"
	if blocking {
		b = "true"
	}
	controlMetrics.MistTriggers.WithLabelValues(triggerType, b, status).Inc()
}

func incRelayForward(commandType, status string) {
	if controlMetrics == nil || controlMetrics.RelayForwards == nil {
		return
	}
	controlMetrics.RelayForwards.WithLabelValues(commandType, status).Inc()
}

func incArtifactSyncOutcome(outcome string) {
	if controlMetrics == nil || controlMetrics.ArtifactSyncOutcomes == nil {
		return
	}
	controlMetrics.ArtifactSyncOutcomes.WithLabelValues(outcome).Inc()
}

func ObserveArtifactDeletionOutcome(outcome string) {
	if controlMetrics == nil || controlMetrics.ArtifactDeletionOutcomes == nil {
		return
	}
	controlMetrics.ArtifactDeletionOutcomes.WithLabelValues(outcome).Inc()
}

func incAdmissionPayloadCrypto(format, result string) {
	if controlMetrics == nil || controlMetrics.AdmissionPayloadCrypto == nil {
		return
	}
	controlMetrics.AdmissionPayloadCrypto.WithLabelValues(format, result).Inc()
}

func incNodeAdmissionEvent(operation, result string) {
	if controlMetrics == nil || controlMetrics.NodeAdmissionEvents == nil {
		return
	}
	controlMetrics.NodeAdmissionEvents.WithLabelValues(operation, result).Inc()
}
