package grpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_analytics_query/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

func buildTestConfig(t *testing.T, certFile, keyFile string) GRPCServerConfig {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	logger.ExitFunc = func(code int) { panic(fmt.Sprintf("logger exited the process with code %d", code)) }
	return GRPCServerConfig{
		Logger: logger,
		Metrics: &metrics.Metrics{
			GRPCRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_grpc_requests_total"}, []string{"method", "status"}),
			GRPCDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "test_grpc_duration_seconds"}, []string{"method"}),
		},
		CertFile: certFile,
		KeyFile:  keyFile,
	}
}

func buildServer(t *testing.T, ctx context.Context, cfg GRPCServerConfig) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("fatal exit instead of an error: %v", r)
			t.Fatalf("NewGRPCServer must return an error to server.Run, not exit: %v", r)
		}
	}()
	srv, buildErr := NewGRPCServer(ctx, cfg)
	if buildErr == nil && srv != nil {
		srv.Stop()
	}
	return buildErr
}

// A TLS configuration error is returned to server.Run, which stops the
// process through its normal shutdown path.
func TestNewGRPCServerReturnsTLSErrors(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	for _, path := range []string{cert, key} {
		if err := os.WriteFile(path, []byte("not a pem block"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := buildServer(t, context.Background(), buildTestConfig(t, cert, key)); err == nil {
		t.Fatal("invalid TLS files must fail the build")
	}
}

// Shutdown cancels the build context; a build still waiting for TLS files
// returns at once instead of waiting out its own timeout.
func TestNewGRPCServerHonoursBuildContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := buildServer(t, ctx, buildTestConfig(t, filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key")))
	if err == nil {
		t.Fatal("a cancelled build context must fail the build")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("build returned %s after cancellation", elapsed)
	}
}
