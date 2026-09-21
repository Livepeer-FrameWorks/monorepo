package appconfig

import "sync/atomic"

var (
	source atomic.Pointer[func() *Foghorn]
	unset  Foghorn
)

// Install makes Current read the configuration from get, normally the Get
// method of the config.Live that main reloads on SIGHUP. It returns a function
// that reinstates the previously installed source.
func Install(get func() *Foghorn) (restore func()) {
	var next *func() *Foghorn
	if get != nil {
		next = &get
	}
	previous := source.Swap(next)
	return func() { source.Store(previous) }
}

// Current returns the configuration from the installed source. Before a source
// is installed, or when it returns nil, Current returns a zero Foghorn in which
// every setting is unset. Callers must not modify the result.
func Current() *Foghorn {
	if get := source.Load(); get != nil {
		if cfg := (*get)(); cfg != nil {
			return cfg
		}
	}
	return &unset
}
