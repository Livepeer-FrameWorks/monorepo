package handlers

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"frameworks/api_assets/internal/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
)

// An S3-compatible store returns no checksums, so reads validate checksums only
// when asked; the SDK then has no skipped-validation warning to log per read.
func TestAssetHandlerS3CompatibleStoreSkipsChecksumValidation(t *testing.T) {
	counter := func(name string) prometheus.Counter { return prometheus.NewCounter(prometheus.CounterOpts{Name: name}) }
	h, err := NewAssetHandler(S3Config{Bucket: "b", Region: "eu-central", Endpoint: "https://objects.example.test", AccessKey: "a", SecretKey: "s"},
		cache.NewLRU(1<<20, time.Minute), logging.NewLogger(), counter("hits"), counter("misses"), counter("errors"))
	if err != nil {
		t.Fatal(err)
	}
	client, ok := h.s3.(*s3.Client)
	if !ok {
		t.Fatalf("store client is %T", h.s3)
	}
	options := client.Options()
	if options.ResponseChecksumValidation != aws.ResponseChecksumValidationWhenRequired || options.RequestChecksumCalculation != aws.RequestChecksumCalculationWhenRequired {
		t.Fatalf("checksum modes = response %v request %v, want when-required", options.ResponseChecksumValidation, options.RequestChecksumCalculation)
	}
}
