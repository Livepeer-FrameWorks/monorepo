package server

import (
	"net/http"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// reloadFailureCount reads config_reload_failures_total{service} from the
// process registry the service /metrics endpoint serves.
func reloadFailureCount(t *testing.T, service string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "config_reload_failures_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "service" && label.GetValue() == service {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// A SIGHUP whose configuration fails to decode keeps the previous snapshot,
// and the failure is visible three ways: an Error log naming the keys (never
// their values), config_reload_failures_total, and /debug/config.
func TestReloadFailureIsLoggedCountedAndShownInDebugConfig(t *testing.T) {
	resetReloadFnsForTest(t)
	t.Cleanup(func() { resetReloadFnsForTest(t) })
	const service = "svc-reload-failure"

	RegisterReload(func() error {
		return &config.LoadError{Missing: []string{"PURSER_TEST_REQUIRED"}, Invalid: []string{"PURSER_TEST_BREAKER (want an integer)"}}
	})
	logger := logging.NewLogger()
	hook := logtest.NewLocal(logger)
	before := reloadFailureCount(t, service)

	stop := startReloadListener(logger, service)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	var failure *logrus.Entry
	deadline := time.Now().Add(2 * time.Second)
	for failure == nil && time.Now().Before(deadline) {
		for _, entry := range hook.AllEntries() {
			if entry.Level == logrus.ErrorLevel {
				failure = entry
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if failure == nil {
		t.Fatalf("no Error log for the failed reload; entries: %v", hook.AllEntries())
	}
	keys, _ := failure.Data["keys"].([]string)
	if !slices.Equal(keys, []string{"PURSER_TEST_BREAKER", "PURSER_TEST_REQUIRED"}) {
		t.Fatalf("logged keys = %v, want both key names", failure.Data["keys"])
	}

	if got := reloadFailureCount(t, service) - before; got != 1 {
		t.Fatalf("config_reload_failures_total{service=%q} rose by %v, want 1", service, got)
	}

	spec := testRouterSpec(t, service)
	spec.DebugToken = "service-token"
	spec.DebugConfig = func() any { return &debugTestConfig{Mode: "on"} }
	spec.DebugConfigOptions = config.Options{Lookup: func(string) (string, bool) { return "", false }}
	w := serve(t, NewServiceRouter(spec), http.MethodGet, "/debug/config", "127.0.0.1:1", map[string]string{"Authorization": "Bearer service-token"})
	body := w.Body.String()
	for _, want := range []string{`"last_reload_failure"`, "PURSER_TEST_REQUIRED", "PURSER_TEST_BREAKER", `"at"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("/debug/config lacks %s: %s", want, body)
		}
	}
}

// An env file the parser rejects must not put its content in the error, since
// the parser quotes the offending value.
func TestReloadFromFileParseErrorOmitsFileContent(t *testing.T) {
	path := t.TempDir() + "/svc.env"
	if err := os.WriteFile(path, []byte("SECRET_KEY=\"hunter2-unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.ReloadFromFile(path)
	if err == nil {
		t.Fatal("unterminated quote must fail")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("reload error leaks file content: %v", err)
	}
}
