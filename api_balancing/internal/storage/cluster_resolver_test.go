package storage

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
)

func TestS3BackingEqual_FullTuple(t *testing.T) {
	cases := []struct {
		name string
		a, b S3Backing
		want bool
	}{
		{
			name: "identical",
			a:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"},
			want: true,
		},
		{
			name: "case/whitespace in endpoint/region is a DIFFERENT descriptor (byte-exact, matches immutable identity)",
			a:    S3Backing{Bucket: "frameworks", Endpoint: " https://S3.US-EAST-1.amazonaws.com ", Region: " US-EAST-1 "},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"},
			want: false,
		},
		{
			name: "empty region defaults to us-east-1 (the ONLY normalization)",
			a:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.amazonaws.com", Region: ""},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.amazonaws.com", Region: "us-east-1"},
			want: true,
		},
		{
			name: "same bucket, different endpoint — must NOT match (MinIO/R2 collision)",
			a:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "us-east-1"},
			want: false,
		},
		{
			name: "same bucket + endpoint, different region",
			a:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1"},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "fsn1"},
			want: false,
		},
		{
			name: "different bucket",
			a:    S3Backing{Bucket: "frameworks-prod", Region: "us-east-1"},
			b:    S3Backing{Bucket: "frameworks-staging", Region: "us-east-1"},
			want: false,
		},
		{
			name: "both empty endpoints (AWS default) treated equal",
			a:    S3Backing{Bucket: "frameworks", Region: "us-east-1"},
			b:    S3Backing{Bucket: "frameworks", Region: "us-east-1"},
			want: true,
		},
		{
			name: "same provider tuple, DIFFERENT prefix — must NOT match (shared bucket, distinct keyspace)",
			a:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1", Prefix: "prod"},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1", Prefix: "staging"},
			want: false,
		},
		{
			name: "same provider tuple + same prefix — match",
			a:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1", Prefix: "prod"},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1", Prefix: "prod"},
			want: true,
		},
		{
			name: "prefix compared EXACTLY — whitespace/case is a different keyspace",
			a:    S3Backing{Bucket: "frameworks", Region: "nbg1", Prefix: "prod"},
			b:    S3Backing{Bucket: "frameworks", Region: "nbg1", Prefix: " Prod"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Equal(tc.b); got != tc.want {
				t.Fatalf("Equal: got %v, want %v\na=%+v\nb=%+v", got, tc.want, tc.a, tc.b)
			}
		})
	}
}

// resolverFixture is a small builder for ClusterResolver state. Defaults to a
// platform-cluster-served, local-S3-present setup; tests override individual
// fields.
type resolverFixture struct {
	localCluster    string
	servedClusters  map[string]bool
	localS3Backing  S3Backing
	localS3Present  bool
	advertised      map[string]S3Backing
	rejectedCounter *prometheus.CounterVec
}

func (f *resolverFixture) build() *ClusterResolver {
	if f.servedClusters == nil {
		f.servedClusters = map[string]bool{}
	}
	if f.advertised == nil {
		f.advertised = map[string]S3Backing{}
	}
	return &ClusterResolver{
		LocalClusterID:       f.localCluster,
		LocalClusterServed:   func(id string) bool { return f.servedClusters[id] },
		LocalS3Backing:       f.localS3Backing,
		LocalS3ClientPresent: f.localS3Present,
		AdvertisedBacking: func(id string) (S3Backing, bool) {
			b, ok := f.advertised[id]
			return b, ok
		},
		Metrics: f.rejectedCounter,
	}
}

func newRejectedCounter(t *testing.T) *prometheus.CounterVec {
	t.Helper()
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_service_resolution_rejected_total",
		Help: "test counter",
	}, []string{"reason", "service"})
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestResolveOriginDurable_OriginAdvertisedAndLocallyMintable_MintsLocal(t *testing.T) {
	backing := S3Backing{Bucket: "frameworks", Region: "eu-central-1"}
	f := &resolverFixture{
		localCluster:   "platform-eu",
		servedClusters: map[string]bool{"platform-eu": true},
		localS3Backing: backing,
		localS3Present: true,
		advertised:     map[string]S3Backing{"platform-eu": backing},
	}
	cluster, mode := f.build().ResolveOriginDurable("platform-eu")
	if cluster != "platform-eu" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (platform-eu, local)", cluster, mode)
	}
}

func TestResolveOriginDurable_OriginInAnotherCell_DelegatesToOriginNeverLocal(t *testing.T) {
	// A US cell asked to durably store an artifact produced in EU must target EU, even though this cell has its own
	// working S3 client. Storing into the local (US) bucket would move the artifact out of its origin cell.
	f := &resolverFixture{
		localCluster:   "platform-us",
		servedClusters: map[string]bool{"platform-us": true},
		localS3Backing: S3Backing{Bucket: "frameworks-us", Region: "us-east-1"},
		localS3Present: true,
		advertised: map[string]S3Backing{
			"platform-eu": {Bucket: "frameworks-eu", Region: "eu-central-1"},
			"platform-us": {Bucket: "frameworks-us", Region: "us-east-1"},
		},
	}
	cluster, mode := f.build().ResolveOriginDurable("platform-eu")
	if cluster != "platform-eu" || mode != StorageMintViaFederation {
		t.Fatalf("got (%q, %s); want (platform-eu, federation)", cluster, mode)
	}
}

func TestResolveOriginDurable_SameBucketDifferentEndpoint_Delegates(t *testing.T) {
	// Both clusters declare a bucket called "frameworks" but on different endpoints; minting against the wrong
	// endpoint produces opaque 403s.
	f := &resolverFixture{
		localCluster:   "platform-us",
		servedClusters: map[string]bool{"platform-us": true, "selfhost-eu": true},
		localS3Backing: S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"},
		localS3Present: true,
		advertised: map[string]S3Backing{
			"selfhost-eu": {Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1"},
		},
	}
	cluster, mode := f.build().ResolveOriginDurable("selfhost-eu")
	if cluster != "selfhost-eu" || mode != StorageMintViaFederation {
		t.Fatalf("got (%q, %s); want (selfhost-eu, federation)", cluster, mode)
	}
}

func TestResolveOriginDurable_SameProviderDifferentPrefix_Delegates(t *testing.T) {
	// Same bucket/endpoint/region but a different prefix is a different keyspace: minting locally would write under
	// this cell's prefix at a key the origin can never address.
	f := &resolverFixture{
		localCluster:   "platform-eu",
		servedClusters: map[string]bool{"platform-eu": true, "selfhost-eu": true},
		localS3Backing: S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1", Prefix: "prod"},
		localS3Present: true,
		advertised: map[string]S3Backing{
			"selfhost-eu": {Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1", Prefix: "staging"},
		},
	}
	cluster, mode := f.build().ResolveOriginDurable("selfhost-eu")
	if cluster != "selfhost-eu" || mode != StorageMintViaFederation {
		t.Fatalf("got (%q, %s); want (selfhost-eu, federation)", cluster, mode)
	}
}

func TestResolveOriginDurable_UnadvertisedOriginIsLocalCluster_MintsLocal(t *testing.T) {
	f := &resolverFixture{
		localCluster:   "central-primary",
		servedClusters: map[string]bool{"central-primary": true},
		localS3Backing: S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		localS3Present: true,
	}
	cluster, mode := f.build().ResolveOriginDurable("central-primary")
	if cluster != "central-primary" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (central-primary, local)", cluster, mode)
	}
}

func TestResolveOriginDurable_UnadvertisedOriginServedByThisPool_MintsLocal(t *testing.T) {
	// A cell serving several clusters stores for each of them in its own backend when the origin advertises no
	// dedicated backing.
	f := &resolverFixture{
		localCluster:   "platform-eu",
		servedClusters: map[string]bool{"platform-eu": true, "platform-eu-2": true},
		localS3Backing: S3Backing{Bucket: "frameworks-eu", Region: "eu-central-1"},
		localS3Present: true,
	}
	cluster, mode := f.build().ResolveOriginDurable("platform-eu-2")
	if cluster != "platform-eu-2" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (platform-eu-2, local)", cluster, mode)
	}
}

func TestResolveOriginDurable_UnadvertisedForeignOrigin_UnavailableNoLocalFallback(t *testing.T) {
	counter := newRejectedCounter(t)
	f := &resolverFixture{
		localCluster:    "platform-us",
		servedClusters:  map[string]bool{"platform-us": true},
		localS3Backing:  S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		localS3Present:  true,
		rejectedCounter: counter,
	}
	cluster, mode := f.build().ResolveOriginDurable("platform-eu")
	if cluster != "" || mode != StorageUnavailable {
		t.Fatalf("got (%q, %s); want (\"\", unavailable): an origin without storage must not fall back to this cell", cluster, mode)
	}
	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "storage")); got != 1 {
		t.Fatalf("service_unavailable counter = %v, want 1", got)
	}
}

func TestResolveOriginDurable_EmptyOrigin_Unavailable(t *testing.T) {
	counter := newRejectedCounter(t)
	backing := S3Backing{Bucket: "frameworks", Region: "us-east-1"}
	f := &resolverFixture{
		localCluster:    "central-primary",
		servedClusters:  map[string]bool{"central-primary": true},
		localS3Backing:  backing,
		localS3Present:  true,
		advertised:      map[string]S3Backing{"central-primary": backing},
		rejectedCounter: counter,
	}
	cluster, mode := f.build().ResolveOriginDurable("  ")
	if cluster != "" || mode != StorageUnavailable {
		t.Fatalf("got (%q, %s); want (\"\", unavailable) for an unknown origin", cluster, mode)
	}
	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "storage")); got != 1 {
		t.Fatalf("service_unavailable counter = %v, want 1", got)
	}
}

func TestResolveOriginDurable_LocalOriginWithoutS3Client_Unavailable(t *testing.T) {
	f := &resolverFixture{
		localCluster:   "central-primary",
		servedClusters: map[string]bool{"central-primary": true},
	}
	cluster, mode := f.build().ResolveOriginDurable("central-primary")
	if cluster != "" || mode != StorageUnavailable {
		t.Fatalf("got (%q, %s); want (\"\", unavailable) without an S3 client", cluster, mode)
	}
}

func TestStorageMintMode_String(t *testing.T) {
	cases := map[StorageMintMode]string{
		StorageMintLocal:         "local",
		StorageMintViaFederation: "federation",
		StorageUnavailable:       "unavailable",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("mode %d String()=%q want %q", mode, got, want)
		}
	}
}
