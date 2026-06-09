package grpc

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"google.golang.org/protobuf/proto"
)

// mediaListSortColumn helpers are a SQL-injection whitelist: any user-supplied
// sort key that is not explicitly recognised must collapse to the default
// column, so raw input can never reach the query. These tests pin both the
// known mappings and — critically — that arbitrary/garbage input falls through
// to the safe default rather than being echoed back.
func TestClipListSortColumn(t *testing.T) {
	cases := map[string]string{
		"title":            "COALESCE(c.title, '')",
		"size_bytes":       "c.size_bytes",
		"expires_at":       "c.retention_until",
		"created_at":       "c.created_at",
		"":                 "c.created_at",
		"c.size_bytes; --": "c.created_at",
		"DROP TABLE":       "c.created_at",
		"Title":            "c.created_at", // case-sensitive: only exact "title" matches
	}
	for in, want := range cases {
		if got := clipListSortColumn(in); got != want {
			t.Errorf("clipListSortColumn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDVRListSortColumn(t *testing.T) {
	cases := map[string]string{
		"title":      "COALESCE(st.title, d.internal_name, '')",
		"size_bytes": "d.size_bytes",
		"expires_at": "d.retention_until",
		"created_at": "d.created_at",
		"":           "d.created_at",
		"d.id;DROP":  "d.created_at",
	}
	for in, want := range cases {
		if got := dvrListSortColumn(in); got != want {
			t.Errorf("dvrListSortColumn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVodListSortColumn(t *testing.T) {
	cases := map[string]string{
		"title":      "COALESCE(title, filename, '')",
		"size_bytes": "size_bytes",
		"expires_at": "retention_until",
		"created_at": "created_at",
		"":           "created_at",
		"1=1":        "created_at",
	}
	for in, want := range cases {
		if got := vodListSortColumn(in); got != want {
			t.Errorf("vodListSortColumn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMediaListSortDirection(t *testing.T) {
	cases := map[string]string{
		"asc":      "ASC",
		"ASC":      "ASC",
		"Asc":      "ASC",
		"desc":     "DESC",
		"":         "DESC",
		"sideways": "DESC", // anything not asc → DESC
	}
	for in, want := range cases {
		if got := mediaListSortDirection(in); got != want {
			t.Errorf("mediaListSortDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

// The process-lifecycle helpers encode the contract that VOD / clip / DVR /
// DVR-finalize / live are NEVER collapsed into one another: each lifecycle owns
// a distinct processes_* column and tier field, and any unknown/empty lifecycle
// defaults to "live". These tests pin that separation so a future edit can't
// silently merge two lifecycles.
func TestNormalizeProcessLifecycle(t *testing.T) {
	cases := map[string]string{
		"live":         processLifecycleLive,
		"dvr":          processLifecycleDVR,
		"clip":         processLifecycleClip,
		"dvr_finalize": processLifecycleDVRFinalize,
		"vod":          processLifecycleVOD,
		"DVR":          processLifecycleDVR,  // case-insensitive
		"  vod  ":      processLifecycleVOD,  // trimmed
		"":             processLifecycleLive, // empty defaults to live
		"bogus":        processLifecycleLive, // unknown defaults to live
	}
	for in, want := range cases {
		if got := normalizeProcessLifecycle(in); got != want {
			t.Errorf("normalizeProcessLifecycle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidProcessLifecycle(t *testing.T) {
	valid := []string{"live", "dvr", "clip", "dvr_finalize", "vod", "VOD", "  clip  "}
	for _, in := range valid {
		if !validProcessLifecycle(in) {
			t.Errorf("validProcessLifecycle(%q) = false, want true", in)
		}
	}
	invalid := []string{"", "bogus", "lives", "dvr-finalize"}
	for _, in := range invalid {
		if validProcessLifecycle(in) {
			t.Errorf("validProcessLifecycle(%q) = true, want false", in)
		}
	}
}

func TestProcessConfigColumn(t *testing.T) {
	cases := map[string]string{
		"live":         "processes_live",
		"dvr":          "processes_dvr",
		"clip":         "processes_clip",
		"dvr_finalize": "processes_dvr_finalize",
		"vod":          "processes_vod",
		"":             "processes_live", // unknown → live column
		"garbage":      "processes_live",
	}
	for in, want := range cases {
		if got := processConfigColumn(in); got != want {
			t.Errorf("processConfigColumn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTierProcessesForLifecycle(t *testing.T) {
	tier := &purserpb.BillingTier{
		ProcessesLive:        "live-cfg",
		ProcessesDvr:         "dvr-cfg",
		ProcessesClip:        "clip-cfg",
		ProcessesDvrFinalize: "dvr-finalize-cfg",
		ProcessesVod:         "vod-cfg",
	}
	cases := map[string]string{
		"live":         "live-cfg",
		"dvr":          "dvr-cfg",
		"clip":         "clip-cfg",
		"dvr_finalize": "dvr-finalize-cfg",
		"vod":          "vod-cfg",
		"unknown":      "live-cfg", // defaults to live field, never blanks
	}
	for in, want := range cases {
		if got := tierProcessesForLifecycle(tier, in); got != want {
			t.Errorf("tierProcessesForLifecycle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestX402NetworkToChainType(t *testing.T) {
	cases := map[string]string{
		"base":         string(auth.ChainBase),
		"base-mainnet": string(auth.ChainBase),
		"base-sepolia": string(auth.ChainBase),
		"BASE":         string(auth.ChainBase), // case-insensitive
		"arbitrum":     string(auth.ChainArbitrum),
		"arbitrum-one": string(auth.ChainArbitrum),
		"ethereum":     string(auth.ChainEthereum),
		"mainnet":      string(auth.ChainEthereum),
		"":             string(auth.ChainEthereum), // unknown defaults to ethereum
		"solana":       string(auth.ChainEthereum),
	}
	for in, want := range cases {
		if got := x402NetworkToChainType(in); got != want {
			t.Errorf("x402NetworkToChainType(%q) = %q, want %q", in, got, want)
		}
	}
}

// routeClusterIDs collects the tenant's reachable cluster IDs in priority order
// (own cluster, official cluster, then peers) while deduping and dropping
// empties. Order and dedup both matter: downstream fan-out trusts the first
// entry as the primary.
func TestRouteClusterIDs(t *testing.T) {
	if got := routeClusterIDs(nil); got != nil {
		t.Errorf("routeClusterIDs(nil) = %v, want nil", got)
	}

	route := &clusterRoute{
		clusterID:         "primary",
		officialClusterID: "official",
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "peer-a"},
			{ClusterId: "primary"}, // duplicate of primary — must be dropped
			{ClusterId: ""},        // empty — must be dropped
			{ClusterId: "peer-b"},
		},
	}
	got := routeClusterIDs(route)
	want := []string{"primary", "official", "peer-a", "peer-b"}
	if len(got) != len(want) {
		t.Fatalf("routeClusterIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routeClusterIDs = %v, want %v (mismatch at %d)", got, want, i)
		}
	}
}

func TestServiceInstanceAddr(t *testing.T) {
	if got := serviceInstanceAddr(nil); got != "" {
		t.Errorf("serviceInstanceAddr(nil) = %q, want empty", got)
	}
	if got := serviceInstanceAddr(&quartermasterpb.ServiceInstance{Port: proto.Int32(8080)}); got != "" {
		t.Errorf("serviceInstanceAddr(no host) = %q, want empty", got)
	}
	if got := serviceInstanceAddr(&quartermasterpb.ServiceInstance{Host: proto.String("node1")}); got != "" {
		t.Errorf("serviceInstanceAddr(no port) = %q, want empty", got)
	}
	if got := serviceInstanceAddr(&quartermasterpb.ServiceInstance{Host: proto.String("node1"), Port: proto.Int32(0)}); got != "" {
		t.Errorf("serviceInstanceAddr(zero port) = %q, want empty", got)
	}
	if got := serviceInstanceAddr(&quartermasterpb.ServiceInstance{Host: proto.String("node1"), Port: proto.Int32(18008)}); got != "node1:18008" {
		t.Errorf("serviceInstanceAddr = %q, want node1:18008", got)
	}
}

// buildPullSourceView projects a stored pull source for the API surface: the
// raw URI must NEVER be returned, only the redacted form (scheme://host).
func TestBuildPullSourceView(t *testing.T) {
	if got := buildPullSourceView("", true, pullsource.ClassPublic, nil); got != nil {
		t.Errorf("buildPullSourceView(empty uri) = %v, want nil", got)
	}

	raw := "rtmp://user:secret@upstream.example.com:1935/live/key"
	view := buildPullSourceView(raw, true, pullsource.ClassPublic, []string{"c1", "c2"})
	if view == nil {
		t.Fatal("buildPullSourceView returned nil for valid uri")
	}
	if view.GetSourceUriRedacted() == raw {
		t.Fatal("buildPullSourceView leaked the raw URI; expected redacted form")
	}
	if want := pullsource.Redact(raw); view.GetSourceUriRedacted() != want {
		t.Errorf("redacted = %q, want %q", view.GetSourceUriRedacted(), want)
	}
	if !view.GetEnabled() {
		t.Error("expected Enabled=true")
	}
	if view.GetClass() != pullsource.ClassPublic.String() {
		t.Errorf("class = %q, want %q", view.GetClass(), pullsource.ClassPublic.String())
	}
	if len(view.GetAllowedClusterIds()) != 2 {
		t.Errorf("allowed cluster ids = %v, want 2 entries", view.GetAllowedClusterIds())
	}
}

func TestPullSourceEnabled(t *testing.T) {
	// nil input defaults to enabled (a stream with a pull source is on unless
	// explicitly disabled).
	if !pullSourceEnabled(nil) {
		t.Error("pullSourceEnabled(nil) = false, want true")
	}
	if !pullSourceEnabled(&commodorepb.PullSourceInput{}) {
		t.Error("pullSourceEnabled(no enabled field) = false, want true")
	}
	enabledFalse := false
	if pullSourceEnabled(&commodorepb.PullSourceInput{Enabled: &enabledFalse}) {
		t.Error("pullSourceEnabled(Enabled=false) = true, want false")
	}
	enabledTrue := true
	if !pullSourceEnabled(&commodorepb.PullSourceInput{Enabled: &enabledTrue}) {
		t.Error("pullSourceEnabled(Enabled=true) = false, want true")
	}
}

func TestFormatRuntimePlacementRejects(t *testing.T) {
	redacted := "rtmp://upstream.example.com"
	rejects := []pullsource.PlacementReject{
		{Reason: pullsource.PlacementRejectEmptyForPrivate},
		{ClusterID: "c-unknown", Reason: pullsource.PlacementRejectUnknownCluster},
		{ClusterID: "c-nopriv", Reason: pullsource.PlacementRejectMissingPrivateCapability},
	}
	got := formatRuntimePlacementRejects(rejects, redacted)

	// Each reason renders a distinct, human-readable clause.
	for _, want := range []string{
		"is private/multicast",
		"c-unknown",
		"not a registered media (edge) cluster",
		"c-nopriv",
		"allow_private_pull_sources=true",
	} {
		if !contains(got, want) {
			t.Errorf("formatRuntimePlacementRejects output %q missing %q", got, want)
		}
	}
	// Three rejects joined with "; ".
	if strings.Count(got, "; ") != 2 {
		t.Errorf("expected 2 separators in %q", got)
	}
}
