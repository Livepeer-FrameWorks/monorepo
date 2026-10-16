// Package vodupload holds caller-input rules for VOD multipart uploads that
// every service on the completion path applies identically.
package vodupload

import (
	"fmt"
	"strings"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// MaxPartNumber is the highest part number S3 accepts in a multipart upload.
const MaxPartNumber = 10000

// Part is one caller-reported completed part.
type Part struct {
	Number int64
	ETag   string
}

// ValidateCompletedParts rejects a part list S3 would refuse at
// CompleteMultipartUpload: no parts, a part number outside 1..MaxPartNumber,
// a repeated part number, or an empty ETag. The returned error message is
// meant for the caller.
func ValidateCompletedParts(parts []Part) error {
	if len(parts) == 0 {
		return fmt.Errorf("parts are required")
	}
	seen := make(map[int64]struct{}, len(parts))
	for i, p := range parts {
		if p.Number < 1 || p.Number > MaxPartNumber {
			return fmt.Errorf("parts[%d]: partNumber must be between 1 and %d", i, MaxPartNumber)
		}
		if _, dup := seen[p.Number]; dup {
			return fmt.Errorf("parts[%d]: partNumber %d is listed more than once", i, p.Number)
		}
		seen[p.Number] = struct{}{}
		if strings.TrimSpace(p.ETag) == "" {
			return fmt.Errorf("parts[%d]: etag is required for partNumber %d", i, p.Number)
		}
	}
	return nil
}

// ValidateProtoParts applies ValidateCompletedParts to a gRPC part list.
func ValidateProtoParts(parts []*sharedpb.VodCompletedPart) error {
	converted := make([]Part, len(parts))
	for i, p := range parts {
		converted[i] = Part{Number: int64(p.GetPartNumber()), ETag: p.GetEtag()}
	}
	return ValidateCompletedParts(converted)
}
