package placement

import (
	"strings"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func preparationWireFixture(t *testing.T, now time.Time) *placementpb.PreparePlacementRequest {
	t.Helper()
	attempt, err := NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	return &placementpb.PreparePlacementRequest{Query: &placementpb.CandidateQuery{
		TenantId: "tenant", ObjectId: "stream", InternalName: "internal", Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls",
		ClusterIds: []string{"us"}, PolicyDigest: strings.Repeat("a", 64), PolicyRevision: 5, ParentRevision: 3, SourceGeneration: "source"},
		ClusterId: "us", NodeId: "node", AttemptId: attempt, ExpiresAt: timestamppb.New(now.Add(time.Second))}
}

func TestPreparationDeadlineAndResponseCannotRenewEvidence(t *testing.T) {
	now := time.Unix(1800000000, 0)
	req := preparationWireFixture(t, now)
	ack := &placementpb.Preparation{Outcome: placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED,
		TenantId: "tenant", ObjectId: "stream", SourceGeneration: "source", ClusterId: "us", NodeId: "node", Protocol: "hls",
		PolicyRevision: 5, ParentRevision: 3, PolicyDigest: req.Query.PolicyDigest, AttemptId: req.AttemptId,
		ExpiresAt: timestamppb.New(now.Add(time.Second)), Endpoint: "https://node/hls/stream/index.m3u8"}
	if err := ValidatePreparationResponse(req, ack, now); err != nil {
		t.Fatal(err)
	}
	for name, expiry := range map[string]*timestamppb.Timestamp{
		"missing": nil, "invalid": {Seconds: 1 << 62}, "expired": timestamppb.New(now),
		"too_long": timestamppb.New(now.Add(30*time.Second + time.Nanosecond)),
	} {
		t.Run(name, func(t *testing.T) {
			bad := proto.CloneOf(req)
			bad.ExpiresAt = expiry
			if err := ValidatePreparationDeadline(bad, now); err == nil {
				t.Fatal("invalid decision lifetime accepted")
			}
		})
	}
	for name, change := range map[string]func(*placementpb.Preparation){
		"tenant":                    func(p *placementpb.Preparation) { p.TenantId = "other" },
		"object":                    func(p *placementpb.Preparation) { p.ObjectId = "other" },
		"source":                    func(p *placementpb.Preparation) { p.SourceGeneration = "other" },
		"cluster":                   func(p *placementpb.Preparation) { p.ClusterId = "other" },
		"node":                      func(p *placementpb.Preparation) { p.NodeId = "other" },
		"protocol":                  func(p *placementpb.Preparation) { p.Protocol = "whep" },
		"attempt":                   func(p *placementpb.Preparation) { p.AttemptId = "other" },
		"policy":                    func(p *placementpb.Preparation) { p.PolicyRevision++ },
		"parent":                    func(p *placementpb.Preparation) { p.ParentRevision++ },
		"digest":                    func(p *placementpb.Preparation) { p.PolicyDigest = strings.Repeat("b", 64) },
		"negative_tenant_authority": func(p *placementpb.Preparation) { p.TenantAuthorityVersion = -1 },
		"negative_object_authority": func(p *placementpb.Preparation) { p.ObjectAuthorityVersion = -1 },
		"missing_object_authority":  func(p *placementpb.Preparation) { p.TenantAuthorityVersion = 3 },
		"missing_tenant_authority":  func(p *placementpb.Preparation) { p.ObjectAuthorityVersion = 5 },
		"renewed": func(p *placementpb.Preparation) {
			p.ExpiresAt = timestamppb.New(req.ExpiresAt.AsTime().Add(time.Nanosecond))
		},
		"expired":          func(p *placementpb.Preparation) { p.ExpiresAt = timestamppb.New(now) },
		"missing_expiry":   func(p *placementpb.Preparation) { p.ExpiresAt = nil },
		"unknown_outcome":  func(p *placementpb.Preparation) { p.Outcome = 99 },
		"missing_endpoint": func(p *placementpb.Preparation) { p.Endpoint = "" },
		"refused_endpoint": func(p *placementpb.Preparation) {
			p.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE
		},
		"unknown_fields": func(p *placementpb.Preparation) { p.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01}) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := proto.CloneOf(ack)
			change(bad)
			if err := ValidatePreparationResponse(req, bad, now); err == nil {
				t.Fatal("unbound acknowledgement accepted")
			}
		})
	}
	if err := ValidatePreparationResponse(req, ack, req.ExpiresAt.AsTime()); err == nil {
		t.Fatal("replay renewed an expired attempt")
	}
	ack.Outcome, ack.Endpoint = placementpb.PreparationOutcome_PREPARATION_OUTCOME_CAPACITY_EXHAUSTED, ""
	if err := ValidatePreparationResponse(req, ack, now); err != nil {
		t.Fatal(err)
	}
	ack.Ready = true
	if err := ValidatePreparationResponse(req, ack, now); err == nil {
		t.Fatal("refused preparation claimed readiness")
	}
}
