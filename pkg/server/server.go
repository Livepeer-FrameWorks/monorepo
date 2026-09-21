package server

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/gin-gonic/gin"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// ReloadCallback is fired when Run receives SIGHUP. Callbacks re-read
// file-based config or rotate TLS material; they must not block (run any
// work in a goroutine) and should return any error for logging. Returning
// an error does not abort subsequent callbacks — every registered hook
// runs on every SIGHUP.
type ReloadCallback func() error

var (
	reloadMu  sync.Mutex
	reloadFns []ReloadCallback
)

// RegisterReload appends a callback that fires whenever the process
// receives SIGHUP while Run is serving. Registration order is preserved;
// callbacks run sequentially. Safe to call from init() or from goroutines
// while Run is serving.
//
// Even when no callback is registered, Run still installs a SIGHUP
// listener — that's what neuters Go's default-terminate disposition for
// the signal cluster-wide, so `systemctl reload <service>` is a true
// no-op rather than a kill for every Go service.
func RegisterReload(fn ReloadCallback) {
	if fn == nil {
		return
	}
	reloadMu.Lock()
	reloadFns = append(reloadFns, fn)
	reloadMu.Unlock()
}

// snapshotReloadFns returns a copy of the current callback list so the
// reload loop can iterate without holding the mutex.
func snapshotReloadFns() []ReloadCallback {
	reloadMu.Lock()
	defer reloadMu.Unlock()
	out := make([]ReloadCallback, len(reloadFns))
	copy(out, reloadFns)
	return out
}

// startReloadListener installs a SIGHUP handler that dispatches every
// signal to the current snapshot of registered ReloadCallbacks. Callbacks
// run sequentially in registration order; a returned error is logged and
// does not abort the remaining callbacks for that signal.
//
// Returns a stop function that detaches the signal handler and waits for
// the dispatch goroutine to drain. Safe to call from tests directly so the
// SIGHUP-doesn't-terminate property can be verified without spinning up a
// real HTTP server.
func startReloadListener(logger logging.Logger, serviceName string) func() {
	configReloadFailures.WithLabelValues(serviceName).Add(0)
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range reloadCh {
			for _, fn := range snapshotReloadFns() {
				if err := fn(); err != nil {
					recordReloadFailure(logger, serviceName, err)
				}
			}
		}
	}()
	return func() {
		signal.Stop(reloadCh)
		close(reloadCh)
		<-done
	}
}

type trailingSlashFallbackKey struct{}

// HandleOptionalTrailingSlash registers a route for both slash spellings.
func HandleOptionalTrailingSlash(routes gin.IRoutes, method, relativePath string, handlers ...gin.HandlerFunc) {
	routes.Handle(method, relativePath, handlers...)
	if alternate := alternateTrailingSlashPath(relativePath); alternate != relativePath {
		routes.Handle(method, alternate, handlers...)
	}
}

func retryAlternateTrailingSlashPath(router *gin.Engine, c *gin.Context) {
	if c.Request == nil || c.Request.URL == nil {
		return
	}
	if c.Request.Context().Value(trailingSlashFallbackKey{}) == true {
		return
	}
	alternate := alternateTrailingSlashPath(c.Request.URL.Path)
	if alternate == c.Request.URL.Path {
		return
	}
	c.Request.URL.Path = alternate
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), trailingSlashFallbackKey{}, true))
	c.Status(http.StatusOK)
	router.HandleContext(c)
}

func alternateTrailingSlashPath(path string) string {
	switch {
	case path == "" || path == "/":
		return path
	case strings.HasSuffix(path, "/"):
		return strings.TrimSuffix(path, "/")
	default:
		return path + "/"
	}
}
