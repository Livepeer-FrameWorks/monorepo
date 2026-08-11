package main

import (
	"errors"
	"testing"

	"frameworks/api_assets/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestResolveChandlerS3Serving(t *testing.T) {
	log := logging.NewLogger()
	adoptUnused := func() error {
		t.Fatalf("adopt must not be called without a CLUSTER_ID")
		return nil
	}

	t.Run("no cluster id + env bucket fails closed (S3 disabled)", func(t *testing.T) {
		cfg := handlers.S3Config{Bucket: "env-bucket", Endpoint: "http://minio", Prefix: "p"}
		if err := resolveChandlerS3Serving("", false, adoptUnused, &cfg, log); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bucket != "" || cfg.Endpoint != "" || cfg.Prefix != "" {
			t.Fatalf("cluster-less env S3 must be disabled (fail closed), got %+v", cfg)
		}
	})

	t.Run("no cluster id + env bucket + explicit dev opt-in retains env S3", func(t *testing.T) {
		cfg := handlers.S3Config{Bucket: "env-bucket", Endpoint: "http://minio", Prefix: "p"}
		if err := resolveChandlerS3Serving("", true, adoptUnused, &cfg, log); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bucket != "env-bucket" {
			t.Fatalf("dev opt-in must retain the env descriptor, got %+v", cfg)
		}
	})

	t.Run("no cluster id + no bucket is a no-op", func(t *testing.T) {
		cfg := handlers.S3Config{}
		if err := resolveChandlerS3Serving("", false, adoptUnused, &cfg, log); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bucket != "" {
			t.Fatalf("no bucket must stay empty, got %+v", cfg)
		}
	})

	t.Run("cluster id + adopt succeeds retains the adopted descriptor", func(t *testing.T) {
		cfg := handlers.S3Config{}
		adopt := func() error { cfg.Bucket = "qm-bucket"; return nil }
		if err := resolveChandlerS3Serving("cluster-eu", false, adopt, &cfg, log); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bucket != "qm-bucket" {
			t.Fatalf("adopted descriptor must be kept, got %+v", cfg)
		}
	})

	t.Run("cluster id + no descriptor disables S3 (not fatal)", func(t *testing.T) {
		cfg := handlers.S3Config{Bucket: "stale"}
		adopt := func() error { return errClusterNoS3Descriptor }
		if err := resolveChandlerS3Serving("cluster-eu", false, adopt, &cfg, log); err != nil {
			t.Fatalf("no-descriptor must not error, got %v", err)
		}
		if cfg.Bucket != "" {
			t.Fatalf("no-descriptor must disable S3, got %+v", cfg)
		}
	})

	t.Run("cluster id + lookup failure returns an error (caller fails closed)", func(t *testing.T) {
		cfg := handlers.S3Config{}
		adopt := func() error { return errors.New("quartermaster unreachable") }
		if err := resolveChandlerS3Serving("cluster-eu", false, adopt, &cfg, log); err == nil {
			t.Fatal("an authority-lookup failure must return an error so the caller fails closed")
		}
	})
}
