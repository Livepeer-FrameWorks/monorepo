package storage

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// An S3-compatible store returns no checksums, so reads validate checksums only
// when asked; the SDK then has no skipped-validation warning to log per read.
func TestS3CompatibleStoreSkipsChecksumValidation(t *testing.T) {
	c, err := NewS3Client(S3Config{Bucket: "b", Region: "eu-central", Endpoint: "https://objects.example.test", AccessKey: "a", SecretKey: "s"}, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	client, ok := c.client.(*s3.Client)
	if !ok {
		t.Fatalf("store client is %T", c.client)
	}
	options := client.Options()
	if options.ResponseChecksumValidation != aws.ResponseChecksumValidationWhenRequired || options.RequestChecksumCalculation != aws.RequestChecksumCalculationWhenRequired {
		t.Fatalf("checksum modes = response %v request %v, want when-required", options.ResponseChecksumValidation, options.RequestChecksumCalculation)
	}
}
