package vodupload

import (
	"strings"
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestValidateCompletedParts(t *testing.T) {
	cases := []struct {
		name    string
		parts   []Part
		wantErr string
	}{
		{name: "valid", parts: []Part{{Number: 2, ETag: `"b"`}, {Number: 1, ETag: `"a"`}}},
		{name: "empty list", parts: nil, wantErr: "parts are required"},
		{name: "empty etag", parts: []Part{{Number: 1, ETag: ""}}, wantErr: "etag is required"},
		{name: "blank etag", parts: []Part{{Number: 1, ETag: "  "}}, wantErr: "etag is required"},
		{name: "zero part number", parts: []Part{{Number: 0, ETag: "x"}}, wantErr: "between 1 and"},
		{name: "negative part number", parts: []Part{{Number: -3, ETag: "x"}}, wantErr: "between 1 and"},
		{name: "part number above S3 max", parts: []Part{{Number: MaxPartNumber + 1, ETag: "x"}}, wantErr: "between 1 and"},
		{name: "duplicate part number", parts: []Part{{Number: 1, ETag: "x"}, {Number: 1, ETag: "y"}}, wantErr: "more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCompletedParts(tc.parts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateProtoParts(t *testing.T) {
	if err := ValidateProtoParts([]*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: ""}}); err == nil {
		t.Fatal("expected empty etag to be rejected")
	}
	if err := ValidateProtoParts([]*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: `"abc"`}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
