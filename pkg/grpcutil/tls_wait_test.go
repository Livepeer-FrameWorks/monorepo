package grpcutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForServerTLSFilesNoopWhenInsecure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := WaitForServerTLSFiles(ctx, ServerTLSConfig{AllowInsecure: true}, nil); err != nil {
		t.Fatalf("WaitForServerTLSFiles returned error: %v", err)
	}
}

func TestWaitForServerTLSFilesWaitsForFiles(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(certFile, []byte("cert"), 0o600)
		_ = os.WriteFile(keyFile, []byte("key"), 0o600)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := WaitForServerTLSFiles(ctx, ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	}, nil); err != nil {
		t.Fatalf("WaitForServerTLSFiles returned error: %v", err)
	}
}

func TestWaitForServerTLSFilesTimesOut(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := WaitForServerTLSFiles(ctx, ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	}, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
