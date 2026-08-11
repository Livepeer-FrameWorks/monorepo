package federation

import "testing"

// S3Backing.Equal is the mint-routing comparator: two descriptors are the same backend ONLY when
// bucket/endpoint/prefix match byte-for-byte, with region's sole normalization being empty→us-east-1. Case-folding
// or trimming here would disagree with control.BackendFingerprint / the immutable-backend boot guard and could let a
// remote store be treated as locally mintable (or hide a material repoint). This pins that contract directly.
func TestS3Backing_Equal_ByteExact(t *testing.T) {
	base := S3Backing{Bucket: "bucket-a", Endpoint: "https://store.example", Region: "us-east-1", Prefix: "prod"}

	cases := []struct {
		name string
		b    S3Backing
		want bool
	}{
		{"identical", S3Backing{Bucket: "bucket-a", Endpoint: "https://store.example", Region: "us-east-1", Prefix: "prod"}, true},
		{"empty region defaults to us-east-1", S3Backing{Bucket: "bucket-a", Endpoint: "https://store.example", Region: "", Prefix: "prod"}, true},
		{"endpoint case differs", S3Backing{Bucket: "bucket-a", Endpoint: "https://Store.Example", Region: "us-east-1", Prefix: "prod"}, false},
		{"bucket case differs", S3Backing{Bucket: "Bucket-A", Endpoint: "https://store.example", Region: "us-east-1", Prefix: "prod"}, false},
		{"prefix whitespace differs", S3Backing{Bucket: "bucket-a", Endpoint: "https://store.example", Region: "us-east-1", Prefix: "prod "}, false},
		{"prefix value differs", S3Backing{Bucket: "bucket-a", Endpoint: "https://store.example", Region: "us-east-1", Prefix: "staging"}, false},
		{"different region", S3Backing{Bucket: "bucket-a", Endpoint: "https://store.example", Region: "eu-central-1", Prefix: "prod"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.Equal(tc.b); got != tc.want {
				t.Fatalf("Equal(%+v) = %v, want %v", tc.b, got, tc.want)
			}
			// Equal must be symmetric.
			if got := tc.b.Equal(base); got != tc.want {
				t.Fatalf("Equal is asymmetric: reverse(%+v) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

// An empty prefix on both sides is a legitimate match (some backends serve from the bucket root); it must not be
// conflated with a present-but-different prefix.
func TestS3Backing_Equal_EmptyPrefixMatches(t *testing.T) {
	a := S3Backing{Bucket: "b", Endpoint: "https://e", Region: "us-east-1", Prefix: ""}
	if !a.Equal(S3Backing{Bucket: "b", Endpoint: "https://e", Region: "us-east-1", Prefix: ""}) {
		t.Fatal("two root-prefix descriptors must be Equal")
	}
	if a.Equal(S3Backing{Bucket: "b", Endpoint: "https://e", Region: "us-east-1", Prefix: "prod"}) {
		t.Fatal("root prefix must NOT equal a present prefix")
	}
}
