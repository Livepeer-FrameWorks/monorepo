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
			name: "case + whitespace differences in endpoint/region normalized",
			a:    S3Backing{Bucket: "frameworks", Endpoint: " https://S3.US-EAST-1.amazonaws.com ", Region: " US-EAST-1 "},
			b:    S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"},
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

func TestClusterResolver_OriginAdvertisedAndLocallyMintable_PicksOriginLocal(t *testing.T) {
	backing := S3Backing{Bucket: "frameworks", Region: "us-east-1"}
	f := &resolverFixture{
		localCluster:   "platform-eu",
		servedClusters: map[string]bool{"platform-eu": true},
		localS3Backing: backing,
		localS3Present: true,
		advertised:     map[string]S3Backing{"platform-eu": backing},
	}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID:   "platform-eu",
		OfficialClusterID: "platform-eu",
		LegacyClusterID:   "platform-eu",
	})
	if cluster != "platform-eu" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (platform-eu, local)", cluster, mode)
	}
}

func TestClusterResolver_OriginAdvertisedButBucketDiffers_DelegatesNotLocalMint(t *testing.T) {
	// Both clusters declare a bucket called "frameworks" but on different
	// endpoints. Resolver MUST NOT confuse these — minting against the wrong
	// endpoint produces opaque 403s.
	originBacking := S3Backing{Bucket: "frameworks", Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1"}
	localBacking := S3Backing{Bucket: "frameworks", Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1"}
	f := &resolverFixture{
		localCluster:   "platform-us",
		servedClusters: map[string]bool{"platform-us": true, "selfhost-eu": true},
		localS3Backing: localBacking,
		localS3Present: true,
		advertised:     map[string]S3Backing{"selfhost-eu": originBacking},
	}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID: "selfhost-eu",
		LegacyClusterID: "platform-us",
	})
	if cluster != "selfhost-eu" || mode != StorageMintViaFederation {
		t.Fatalf("got (%q, %s); want (selfhost-eu, federation) — same bucket name across endpoints must NOT pass local-mint", cluster, mode)
	}
}

func TestClusterResolver_OriginAdvertisedButNotServedHere_Delegates(t *testing.T) {
	originBacking := S3Backing{Bucket: "selfhost-bucket", Region: "eu-central-1"}
	f := &resolverFixture{
		localCluster:   "platform-us",
		servedClusters: map[string]bool{"platform-us": true}, // selfhost-eu NOT served by this pool
		localS3Backing: S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		localS3Present: true,
		advertised:     map[string]S3Backing{"selfhost-eu": originBacking},
	}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID: "selfhost-eu",
		LegacyClusterID: "platform-us",
	})
	if cluster != "selfhost-eu" || mode != StorageMintViaFederation {
		t.Fatalf("got (%q, %s); want (selfhost-eu, federation) — not-served clusters must delegate", cluster, mode)
	}
}

func TestClusterResolver_OriginNotAdvertised_FallsBackToOfficialAdvertised(t *testing.T) {
	officialBacking := S3Backing{Bucket: "frameworks", Region: "us-east-1"}
	f := &resolverFixture{
		localCluster:   "platform-eu",
		servedClusters: map[string]bool{"platform-eu": true},
		localS3Backing: officialBacking,
		localS3Present: true,
		// Origin "selfhost-tenant" advertises NO storage backing.
		advertised: map[string]S3Backing{"platform-eu": officialBacking},
	}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID:   "selfhost-tenant",
		OfficialClusterID: "platform-eu",
		LegacyClusterID:   "platform-eu",
	})
	if cluster != "platform-eu" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (platform-eu, local) — origin lacks backing, official wins", cluster, mode)
	}
}

func TestClusterResolver_NeitherOriginNorOfficialAdvertised_LegacyLocalFallback(t *testing.T) {
	// Existing single-cluster deployment: Foghorn has STORAGE_S3_BUCKET set
	// but Quartermaster doesn't yet advertise S3 metadata for this cluster.
	// The Legacy slot (== p.clusterID) must allow local mint.
	f := &resolverFixture{
		localCluster:   "central-primary",
		servedClusters: map[string]bool{"central-primary": true},
		localS3Backing: S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		localS3Present: true,
		advertised:     map[string]S3Backing{}, // nothing advertises
	}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID:   "central-primary",
		OfficialClusterID: "central-primary",
		LegacyClusterID:   "central-primary",
	})
	if cluster != "central-primary" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (central-primary, local) — legacy slot must fall back when only local Foghorn knows it has S3", cluster, mode)
	}
}

func TestClusterResolver_LegacyFallbackOnlyValidForLocalCluster(t *testing.T) {
	// If the legacy slot is some OTHER cluster (i.e. not p.clusterID) and that
	// cluster doesn't advertise, we must NOT legacy-mint — the local Foghorn's
	// S3 client almost certainly belongs to a different bucket. Resolver
	// must continue past and emit unavailable.
	counter := newRejectedCounter(t)
	f := &resolverFixture{
		localCluster:    "platform-us",
		servedClusters:  map[string]bool{"platform-us": true},
		localS3Backing:  S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		localS3Present:  true,
		advertised:      map[string]S3Backing{}, // nothing advertises
		rejectedCounter: counter,
	}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID: "stale-cluster",
		LegacyClusterID: "stale-cluster", // NOT == localCluster, so legacy path is invalid
	})
	if mode != StorageUnavailable {
		t.Fatalf("got (%q, %s); want (\"\", unavailable) — legacy fallback must require legacy == LocalClusterID", cluster, mode)
	}
	if cluster != "" {
		t.Fatalf("unavailable result must clear cluster id, got %q", cluster)
	}
	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "storage")); got != 1 {
		t.Fatalf("expected service_unavailable counter to increment once, got %v", got)
	}
}

func TestClusterResolver_NoCandidates_Unavailable(t *testing.T) {
	counter := newRejectedCounter(t)
	f := &resolverFixture{rejectedCounter: counter}
	r := f.build()

	cluster, mode := r.Resolve(ResolverInput{})
	if cluster != "" || mode != StorageUnavailable {
		t.Fatalf("got (%q, %s); want (\"\", unavailable) for empty input", cluster, mode)
	}
	if got := counterValue(t, counter.WithLabelValues("service_unavailable", "storage")); got != 1 {
		t.Fatalf("expected service_unavailable counter to increment once on empty input, got %v", got)
	}
}

func TestClusterResolver_DedupesRepeatedCandidate(t *testing.T) {
	// origin == official == legacy == "central-primary"; advertised lookup
	// must be invoked at most once per distinct cluster id.
	calls := map[string]int{}
	r := &ClusterResolver{
		LocalClusterID:       "central-primary",
		LocalClusterServed:   func(id string) bool { return id == "central-primary" },
		LocalS3Backing:       S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		LocalS3ClientPresent: true,
		AdvertisedBacking: func(id string) (S3Backing, bool) {
			calls[id]++
			return S3Backing{Bucket: "frameworks", Region: "us-east-1"}, true
		},
	}

	cluster, mode := r.Resolve(ResolverInput{
		OriginClusterID:   "central-primary",
		OfficialClusterID: "central-primary",
		LegacyClusterID:   "central-primary",
	})
	if cluster != "central-primary" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (central-primary, local)", cluster, mode)
	}
	if calls["central-primary"] != 1 {
		t.Fatalf("AdvertisedBacking should have been called exactly once for the deduped cluster, got %d", calls["central-primary"])
	}
}

func TestClusterResolver_SkipsEmptyCandidates(t *testing.T) {
	backing := S3Backing{Bucket: "frameworks", Region: "us-east-1"}
	f := &resolverFixture{
		localCluster:   "central-primary",
		servedClusters: map[string]bool{"central-primary": true},
		localS3Backing: backing,
		localS3Present: true,
		advertised:     map[string]S3Backing{"central-primary": backing},
	}
	r := f.build()

	// origin and official empty; only legacy populated.
	cluster, mode := r.Resolve(ResolverInput{
		LegacyClusterID: "central-primary",
	})
	if cluster != "central-primary" || mode != StorageMintLocal {
		t.Fatalf("got (%q, %s); want (central-primary, local)", cluster, mode)
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
