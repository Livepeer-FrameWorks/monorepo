package appconfig

import (
	"sync"
	"sync/atomic"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

var (
	installedRuntime atomic.Pointer[config.Live[PurserRuntime]]

	defaultRuntime = sync.OnceValue(func() *PurserRuntime {
		cfg, err := config.Load[PurserRuntime](config.Options{
			Service: "purser",
			Lookup:  func(string) (string, bool) { return "", false },
		})
		if err != nil {
			panic(err)
		}
		return cfg
	})
)

// InstallRuntime makes live the source of Runtime and returns the source it
// replaced, or nil when none was installed. The Purser server installs its
// reloadable runtime configuration before constructing any internal package.
func InstallRuntime(live *config.Live[PurserRuntime]) *config.Live[PurserRuntime] {
	return installedRuntime.Swap(live)
}

// Runtime returns the current runtime configuration snapshot. Callers take one
// snapshot per operation so related values come from the same reload
// generation. Without an installed source it returns the declared defaults.
func Runtime() *PurserRuntime {
	if live := installedRuntime.Load(); live != nil {
		return live.Get()
	}
	return defaultRuntime()
}
