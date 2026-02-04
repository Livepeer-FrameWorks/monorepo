package storage

import "testing"

func TestBuildS3URL(t *testing.T) {
	tests := []struct {
		name     string
		bucket   string
		prefix   string
		key      string
		expected string
	}{
		{
			name:     "with_prefix",
			bucket:   "media-bucket",
			prefix:   "tenant-a",
			key:      "clips/clip-1.mp4",
			expected: "s3://media-bucket/tenant-a/clips/clip-1.mp4",
		},
		{
			name:     "no_prefix",
			bucket:   "media-bucket",
			prefix:   "",
			key:      "vod/asset.mp4",
			expected: "s3://media-bucket/vod/asset.mp4",
		},
		{
			name:     "trim_slashes",
			bucket:   "media-bucket",
			prefix:   "tenant-a/",
			key:      "/dvr/segment",
			expected: "s3://media-bucket/tenant-a/dvr/segment",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &S3Client{config: S3Config{Bucket: test.bucket, Prefix: test.prefix}}
			actual := client.BuildS3URL(test.key)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestBuildClipAndDVRS3Keys(t *testing.T) {
	tests := []struct {
		name     string
		fn       func(*S3Client) string
		expected string
	}{
		{
			name: "clip_key",
			fn: func(c *S3Client) string {
				return c.BuildClipS3Key("tenant", "stream", "cliphash", "mp4")
			},
			expected: "clips/tenant/stream/cliphash.mp4",
		},
		{
			name: "dvr_key",
			fn: func(c *S3Client) string {
				return c.BuildDVRS3Key("tenant", "internal", "dvrhash")
			},
			expected: "dvr/tenant/internal/dvrhash",
		},
	}

	client := &S3Client{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := test.fn(client)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestBuildVodS3Key(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		expected string
	}{
		{
			name:     "with_extension",
			filename: "video.mov",
			expected: "vod/tenant/hash/hash.mov",
		},
		{
			name:     "no_extension",
			filename: "video",
			expected: "vod/tenant/hash/hash.mp4",
		},
		{
			name:     "trailing_dot",
			filename: "video.",
			expected: "vod/tenant/hash/hash.mp4",
		},
	}

	client := &S3Client{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := client.BuildVodS3Key("tenant", "hash", test.filename)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestCalculatePartSize(t *testing.T) {
	tests := []struct {
		name          string
		totalSize     int64
		expectedSize  int64
		expectedCount int
	}{
		{
			name:          "zero_size",
			totalSize:     0,
			expectedSize:  DefaultPartSize,
			expectedCount: 0,
		},
		{
			name:          "single_part",
			totalSize:     10 * 1024 * 1024,
			expectedSize:  DefaultPartSize,
			expectedCount: 1,
		},
		{
			name:          "multiple_parts",
			totalSize:     DefaultPartSize*2 + 1,
			expectedSize:  DefaultPartSize,
			expectedCount: 3,
		},
		{
			name:          "too_many_parts",
			totalSize:     DefaultPartSize*MaxPartCount + 1,
			expectedSize:  21 * 1024 * 1024,
			expectedCount: 9524,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			partSize, partCount := CalculatePartSize(test.totalSize)
			if partSize != test.expectedSize {
				t.Fatalf("expected partSize %d, got %d", test.expectedSize, partSize)
			}
			if partCount != test.expectedCount {
				t.Fatalf("expected partCount %d, got %d", test.expectedCount, partCount)
			}
		})
	}
}
