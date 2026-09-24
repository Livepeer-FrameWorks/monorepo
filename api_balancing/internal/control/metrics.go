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
	// RestreamReconcile records desired-state command delivery and correlated
	// acknowledgements. Labels are bounded operation/outcome enums and never
	// contain tenant, stream, node, or target identifiers.
	RestreamReconcile *prometheus.CounterVec
	// OfflineEffectDeadLetters records terminal offline obligations and late
	// correlated acknowledgements that revive them. Labels: outcome
	// (retained|revived).
	OfflineEffectDeadLetters *prometheus.CounterVec
	// StreamTranscodeDegraded counts transcode processes Mist replaced after a
	// hard failure (PROCESS_REPLACE). Labels: failed_process_type, stream_kind
	// (live|pull|processing|other), replaced ("true"|"false").
	StreamTranscodeDegraded *prometheus.CounterVec
	// ProcessingResultsIgnored counts Helmsman processing results Foghorn
	// refused to apply. Labels: status (reported status), reason (unknown_job|
	// inactive_job|unassigned_job|node_mismatch).
	ProcessingResultsIgnored *prometheus.CounterVec
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

func incStreamTranscodeDegraded(failedProcessType, streamKind string, replaced bool) {
	if controlMetrics == nil || controlMetrics.StreamTranscodeDegraded == nil {
		return
	}
	r := "false"
	if replaced {
		r = "true"
	}
	controlMetrics.StreamTranscodeDegraded.WithLabelValues(failedProcessType, streamKind, r).Inc()
}

func incProcessingResultIgnored(status, reason string) {
	if controlMetrics == nil || controlMetrics.ProcessingResultsIgnored == nil {
		return
	}
	controlMetrics.ProcessingResultsIgnored.WithLabelValues(status, reason).Inc()
}

func incAdmissionPayloadCrypto(format, result string) {
	if controlMetrics == nil || controlMetrics.AdmissionPayloadCrypto == nil {
		return
	}
	controlMetrics.AdmissionPayloadCrypto.WithLabelValues(format, result).Inc()
}

func incRestreamReconcile(operation, outcome string) {
	if controlMetrics == nil || controlMetrics.RestreamReconcile == nil {
		return
	}
	controlMetrics.RestreamReconcile.WithLabelValues(operation, outcome).Inc()
}

// ObserveOfflineEffectDeadLetter records a bounded operational outcome for a
// durable offline obligation. It is exported for the background drain job.
func ObserveOfflineEffectDeadLetter(outcome string) {
	if controlMetrics == nil || controlMetrics.OfflineEffectDeadLetters == nil {
		return
	}
	controlMetrics.OfflineEffectDeadLetters.WithLabelValues(outcome).Inc()
}

func incNodeAdmissionEvent(operation, result string) {
	if controlMetrics == nil || controlMetrics.NodeAdmissionEvents == nil {
		return
	}
	controlMetrics.NodeAdmissionEvents.WithLabelValues(operation, result).Inc()
}
