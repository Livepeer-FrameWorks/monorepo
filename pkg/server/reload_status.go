package server

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// configReloadFailures counts SIGHUP reload callbacks that returned an error.
// A failed reload leaves the previous configuration in effect, so without
// this counter a rejected change is visible only in the log.
var configReloadFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "config_reload_failures_total",
	Help: "SIGHUP configuration reloads that failed; the previous configuration stays in effect.",
}, []string{"service"})

// ReloadFailure is the most recent failed reload, as /debug/config reports it.
// Keys names the configuration keys the failure is about; the error text of a
// configuration decode never contains values.
type ReloadFailure struct {
	At    time.Time `json:"at"`
	Keys  []string  `json:"keys,omitempty"`
	Error string    `json:"error"`
}

var (
	reloadFailureMu   sync.Mutex
	lastReloadFailure *ReloadFailure
)

// recordReloadFailure logs a failed reload callback at Error with the key
// names, counts it, and keeps it for /debug/config.
func recordReloadFailure(logger logging.Logger, service string, err error) {
	keys := config.ErrorKeys(err)
	logger.WithError(err).WithField("service", service).WithField("keys", keys).
		Error("Configuration reload failed; the previous configuration stays in effect")
	configReloadFailures.WithLabelValues(service).Inc()
	reloadFailureMu.Lock()
	lastReloadFailure = &ReloadFailure{At: time.Now().UTC(), Keys: keys, Error: err.Error()}
	reloadFailureMu.Unlock()
}

// LastReloadFailure returns the most recent failed reload, or nil when none
// has failed since the process started.
func LastReloadFailure() *ReloadFailure {
	reloadFailureMu.Lock()
	defer reloadFailureMu.Unlock()
	if lastReloadFailure == nil {
		return nil
	}
	failure := *lastReloadFailure
	failure.Keys = append([]string(nil), lastReloadFailure.Keys...)
	return &failure
}
