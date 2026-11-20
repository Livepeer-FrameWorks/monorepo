package handlers

import (
	"time"

	"frameworks/api_sidecar/internal/netstat"
)

func (pm *PrometheusMonitor) observeNetworkCounters() {
	if pm.networkReader == nil {
		return
	}
	snapshot, err := pm.networkReader.Read()
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	if pm.networkSampler == nil {
		pm.networkSampler = netstat.NewSampler(10 * time.Second)
	}
	if err != nil {
		pm.networkSampler.Invalidate()
		pm.networkRate = netstat.Rate{}
		if monitorLogger != nil && pm.networkReadError != err.Error() {
			monitorLogger.WithError(err).Warn("Network counters unavailable; using Mist bandwidth measurements")
		}
		pm.networkReadError = err.Error()
		return
	}
	pm.networkReadError = ""
	pm.networkRate = pm.networkSampler.Sample(snapshot)
}
