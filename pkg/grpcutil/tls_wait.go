package grpcutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"frameworks/pkg/logging"
)

const serverTLSFilePollInterval = 500 * time.Millisecond

// WaitForServerTLSFiles blocks until configured server TLS files exist and are non-empty.
// It is a no-op when insecure mode is allowed or when file-based TLS is not configured.
func WaitForServerTLSFiles(ctx context.Context, cfg ServerTLSConfig, logger logging.Logger) error {
	certFile := strings.TrimSpace(cfg.CertFile)
	keyFile := strings.TrimSpace(cfg.KeyFile)

	if cfg.AllowInsecure || certFile == "" || keyFile == "" {
		return nil
	}

	if ready, _ := serverTLSFilesReady(certFile, keyFile); ready {
		return nil
	}

	if logger != nil {
		logger.WithFields(logging.Fields{
			"cert_file": certFile,
			"key_file":  keyFile,
		}).Info("Waiting for gRPC server TLS files")
	}

	ticker := time.NewTicker(serverTLSFilePollInterval)
	defer ticker.Stop()

	for {
		ready, missing := serverTLSFilesReady(certFile, keyFile)
		if ready {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for gRPC server TLS files: %s", strings.Join(missing, ", "))
		case <-ticker.C:
		}
	}
}

func serverTLSFilesReady(certFile, keyFile string) (bool, []string) {
	missing := make([]string, 0, 2)
	for _, path := range []string{certFile, keyFile} {
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			missing = append(missing, path)
		}
	}
	return len(missing) == 0, missing
}
