package main

import (
	"testing"

	"frameworks/api_balancing/internal/federation"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
)

// backingFromPeer is the fail-closed gate that decides whether a remote cluster's advertised descriptor is a usable S3
// backing for mint routing. It must yield a backing ONLY for a fully-adopted descriptor (bucket present AND prefix
// present); an incomplete one (no bucket, or a NULL prefix collapsed to "") must report (zero, false) so the
// resolver never local-mints against an ambiguous identity.
func TestBackingFromPeer_FailsClosedOnIncompleteDescriptor(t *testing.T) {
	cases := []struct {
		name       string
		peer       *clusterpeerpb.TenantClusterPeer
		wantOK     bool
		wantPrefix string
	}{
		{
			name:       "fully adopted descriptor yields a backing",
			peer:       &clusterpeerpb.TenantClusterPeer{S3Bucket: "bucket-a", S3Endpoint: "https://store.example", S3Region: "us-east-1", S3Prefix: "prod", S3PrefixPresent: true},
			wantOK:     true,
			wantPrefix: "prod",
		},
		{
			name:       "present-but-empty prefix is a legitimate root keyspace",
			peer:       &clusterpeerpb.TenantClusterPeer{S3Bucket: "bucket-a", S3Endpoint: "https://store.example", S3Region: "us-east-1", S3Prefix: "", S3PrefixPresent: true},
			wantOK:     true,
			wantPrefix: "",
		},
		{
			name:   "bucket present but prefix NOT present fails closed",
			peer:   &clusterpeerpb.TenantClusterPeer{S3Bucket: "bucket-a", S3Endpoint: "https://store.example", S3Region: "us-east-1", S3Prefix: "", S3PrefixPresent: false},
			wantOK: false,
		},
		{
			name:   "no bucket fails closed",
			peer:   &clusterpeerpb.TenantClusterPeer{S3Bucket: "", S3PrefixPresent: true},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backing, ok := backingFromPeer(tc.peer)
			if ok != tc.wantOK {
				t.Fatalf("backingFromPeer ok = %v, want %v (backing=%+v)", ok, tc.wantOK, backing)
			}
			if !ok {
				if backing != (federation.S3Backing{}) {
					t.Fatalf("fail-closed must return the zero backing, got %+v", backing)
				}
				return
			}
			if backing.Prefix != tc.wantPrefix {
				t.Fatalf("backing.Prefix = %q, want %q", backing.Prefix, tc.wantPrefix)
			}
			if backing.Bucket != tc.peer.GetS3Bucket() {
				t.Fatalf("backing.Bucket = %q, want %q", backing.Bucket, tc.peer.GetS3Bucket())
			}
		})
	}
}
