package database

import (
	"errors"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	FailureUndefinedObject    = "undefined_object"
	FailureTypeMismatch       = "type_mismatch"
	FailureConstraint         = "constraint"
	FailureScan               = "scan"
	FailureCapabilityMismatch = "capability_mismatch"
)

var databaseFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "frameworks_database_failures_total",
	Help: "Database contract failures observed by runtime clients.",
}, []string{"service", "engine", "failure"})

func init() {
	prometheus.MustRegister(databaseFailures)
}

// ClassifyDatabaseError returns a bounded contract-failure label. Empty means
// the error is not one of the deploy/schema mismatch classes tracked here.
func ClassifyDatabaseError(err error, scan bool) string {
	if err == nil {
		return ""
	}
	var capabilityErr *CapabilityError
	if errors.As(err, &capabilityErr) {
		return FailureCapabilityMismatch
	}
	state := SQLState(err)
	if state == "42P01" || state == "42703" || state == "42883" || state == "3F000" {
		return FailureUndefinedObject
	}
	if strings.HasPrefix(state, "23") {
		return FailureConstraint
	}
	if state == "42804" || strings.HasPrefix(state, "22") {
		return FailureTypeMismatch
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unknown table") || strings.Contains(message, "unknown identifier") || strings.Contains(message, "does not exist"):
		return FailureUndefinedObject
	case strings.Contains(message, "cannot convert") || strings.Contains(message, "type mismatch") || strings.Contains(message, "invalid input syntax"):
		return FailureTypeMismatch
	case strings.Contains(message, "constraint") || strings.Contains(message, "violates"):
		return FailureConstraint
	case scan || strings.Contains(message, "scan error") || strings.Contains(message, "converting driver.value"):
		return FailureScan
	default:
		return ""
	}
}

// ObserveDatabaseError records only actionable schema/result contract
// failures; operational errors such as timeouts do not pollute this counter.
func ObserveDatabaseError(service string, engine Engine, err error, scan bool) {
	failure := ClassifyDatabaseError(err, scan)
	if failure == "" {
		return
	}
	if service == "" {
		service = "unknown"
	}
	databaseFailures.WithLabelValues(service, string(engine), failure).Inc()
}
