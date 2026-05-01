package storage

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestMapListedParts(t *testing.T) {
	in := []types.Part{
		{PartNumber: aws.Int32(1), ETag: aws.String(`"etag-1"`), Size: aws.Int64(5_242_880)},
		{PartNumber: nil, ETag: aws.String(`"skip-no-number"`)}, // skipped
		{PartNumber: aws.Int32(3), ETag: aws.String("etag-3-unquoted"), Size: aws.Int64(1024)},
		{PartNumber: aws.Int32(4)}, // missing etag and size
	}
	got := mapListedParts(in)
	want := []UploadedPart{
		{PartNumber: 1, ETag: "etag-1", SizeBytes: 5_242_880},
		{PartNumber: 3, ETag: "etag-3-unquoted", SizeBytes: 1024},
		{PartNumber: 4, ETag: "", SizeBytes: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected mapping:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestMapListedParts_Empty(t *testing.T) {
	if got := mapListedParts(nil); len(got) != 0 {
		t.Fatalf("expected empty result for nil, got %+v", got)
	}
}

func TestMissingPartNumbers(t *testing.T) {
	tests := []struct {
		name     string
		have     []UploadedPart
		total    int
		expected []int
	}{
		{
			name:     "none uploaded",
			total:    3,
			expected: []int{1, 2, 3},
		},
		{
			name:     "all uploaded",
			have:     []UploadedPart{{PartNumber: 1}, {PartNumber: 2}, {PartNumber: 3}},
			total:    3,
			expected: []int{},
		},
		{
			name:     "gap in middle",
			have:     []UploadedPart{{PartNumber: 1}, {PartNumber: 3}},
			total:    4,
			expected: []int{2, 4},
		},
		{
			name:     "out-of-order uploaded",
			have:     []UploadedPart{{PartNumber: 5}, {PartNumber: 1}},
			total:    5,
			expected: []int{2, 3, 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MissingPartNumbers(tc.have, tc.total)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("missing parts: got=%v want=%v", got, tc.expected)
			}
		})
	}
}
