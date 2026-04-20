package config

import "sync"

type RuntimeOverrides struct {
	ContextName     string
	ContextExplicit bool

	ConfigPath         string
	ConfigPathExplicit bool

	OutputJSON bool
	NoHints    bool
}

var (
	runtimeMu sync.RWMutex
	runtime   RuntimeOverrides
)

func SetRuntimeOverrides(o RuntimeOverrides) {
	runtimeMu.Lock()
	runtime = o
	runtimeMu.Unlock()
}

func GetRuntimeOverrides() RuntimeOverrides {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	return runtime
}
