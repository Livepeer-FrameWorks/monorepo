package placement

import (
	"math"
	"strings"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPlacementQueryWireValidation(t *testing.T) {
	query := &placementpb.CandidateQuery{TenantId: "tenant", ObjectId: "live_stream:stream", InternalName: "internal-stream", Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls", ClusterIds: []string{"us"}, PolicyDigest: strings.Repeat("a", 64)}
	if err := ValidateCandidateQuery(query); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*placementpb.CandidateQuery){
		"tenant":            func(q *placementpb.CandidateQuery) { q.TenantId = "" },
		"object":            func(q *placementpb.CandidateQuery) { q.ObjectId = "" },
		"stream":            func(q *placementpb.CandidateQuery) { q.InternalName = "" },
		"verb":              func(q *placementpb.CandidateQuery) { q.Verb = 99 },
		"protocol":          func(q *placementpb.CandidateQuery) { q.Protocol = "HLS" },
		"cluster":           func(q *placementpb.CandidateQuery) { q.ClusterIds = nil },
		"duplicate_cluster": func(q *placementpb.CandidateQuery) { q.ClusterIds = []string{"us", "us"} },
		"bound":             func(q *placementpb.CandidateQuery) { q.ClusterIds = make([]string, 4097) },
		"digest":            func(q *placementpb.CandidateQuery) { q.PolicyDigest = strings.Repeat("z", 64) },
		"revision":          func(q *placementpb.CandidateQuery) { q.PolicyRevision = math.MaxUint64 },
		"parent":            func(q *placementpb.CandidateQuery) { q.ParentRevision = math.MaxUint64 },
		"location":          func(q *placementpb.CandidateQuery) { q.ClientLocation = &placementpb.Coordinates{Latitude: math.NaN()} },
		"unknown":           func(q *placementpb.CandidateQuery) { q.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01}) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.CloneOf(query)
			mutate(changed)
			if err := ValidateCandidateQuery(changed); err == nil {
				t.Fatal("invalid query accepted")
			}
		})
	}
	attempt, err := NewPreparationAttemptID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	prepare := &placementpb.PreparePlacementRequest{Query: query, NodeId: "node", ClusterId: "us", AttemptId: attempt, ExpiresAt: timestamppb.New(time.Now().Add(20 * time.Second))}
	if err := ValidatePreparationRequest(prepare); err != nil {
		t.Fatal(err)
	}
	prepare.ClusterId = "eu"
	if err := ValidatePreparationRequest(prepare); err == nil {
		t.Fatal("unrequested destination accepted")
	}
	prepare.ClusterId, prepare.AttemptId = "us", ""
	if err := ValidatePreparationRequest(prepare); err == nil {
		t.Fatal("missing idempotency identity accepted")
	}
}

func TestIngestPreparationWireRequiresUnboundPublisherTemplate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	attempt, err := NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	req := &placementpb.PreparePlacementRequest{Query: &placementpb.CandidateQuery{TenantId: "tenant", ObjectId: "live_stream:stream", InternalName: "internal",
		Verb: placementpb.Verb_VERB_INGEST, Protocol: "rtmp", ClusterIds: []string{"us"}, PolicyDigest: strings.Repeat("a", 64)},
		NodeId: "node", ClusterId: "us", AttemptId: attempt, ExpiresAt: timestamppb.New(now.Add(PreparationLifetime))}
	response := &placementpb.Preparation{Outcome: placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED,
		TenantId: "tenant", ObjectId: "live_stream:stream", ClusterId: "us", NodeId: "node", Protocol: "rtmp", PolicyDigest: req.Query.PolicyDigest,
		AttemptId: attempt, ExpiresAt: req.ExpiresAt, Endpoint: "rtmps://node.example:2935/live/$", PublicBaseUrl: "https://node.example:8443"}
	if err := ValidatePreparationResponse(req, response, now); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", "https://user:secret@node.example", "rtmp://node.example:1935", "https://HOST", "https://node.example/path", "https://node.example?secret=value"} {
		invalid := proto.CloneOf(response)
		invalid.PublicBaseUrl = base
		if err := ValidatePreparationResponse(req, invalid, now); err == nil {
			t.Fatalf("unsafe ingest public origin accepted: %q", base)
		}
	}
	for _, endpoint := range []string{"rtmps://node.example:2935/live/secret", "rtmps://node.example:2935/play/$", "https://node.example/webrtc/$", "rtmps://HOST:2935/live/$", "rtmps://user:secret@node.example/live/$"} {
		changed := proto.CloneOf(response)
		changed.Endpoint = endpoint
		if err := ValidatePreparationResponse(req, changed, now); err == nil {
			t.Fatalf("unsafe ingest acknowledgement accepted: %s", endpoint)
		}
	}
	response.Ready = true
	if err := ValidatePreparationResponse(req, response, now); err == nil {
		t.Fatal("preparation claimed an active publisher")
	}
}

func TestPlacementManagementWireValidation(t *testing.T) {
	review := &placementpb.ReviewChangeRequest{
		Scope:   &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT},
		Updates: []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}},
	}
	if err := ValidateReviewChangeRequest(review); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*placementpb.ReviewChangeRequest){
		"missing_scope":      func(r *placementpb.ReviewChangeRequest) { r.Scope = nil },
		"unknown_scope":      func(r *placementpb.ReviewChangeRequest) { r.Scope.Kind = 99 },
		"tenant_stream":      func(r *placementpb.ReviewChangeRequest) { r.Scope.StreamId = "stream" },
		"tenant_parent":      func(r *placementpb.ReviewChangeRequest) { r.ExpectedParentRevision = 1 },
		"missing_stream":     func(r *placementpb.ReviewChangeRequest) { r.Scope.Kind = placementpb.ScopeKind_SCOPE_KIND_STREAM },
		"revision_exhausted": func(r *placementpb.ReviewChangeRequest) { r.ExpectedRevision = math.MaxInt64 },
		"parent_overflow":    func(r *placementpb.ReviewChangeRequest) { r.ExpectedParentRevision = math.MaxUint64 },
		"missing_updates":    func(r *placementpb.ReviewChangeRequest) { r.Updates = nil },
		"duplicate_verb":     func(r *placementpb.ReviewChangeRequest) { r.Updates = append(r.Updates, proto.CloneOf(r.Updates[0])) },
		"unknown_fields":     func(r *placementpb.ReviewChangeRequest) { r.Scope.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01}) },
		"clear_with_rules":   func(r *placementpb.ReviewChangeRequest) { r.Updates[0].Rules = &placementpb.Rules{SchemaVersion: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := proto.CloneOf(review)
			mutate(bad)
			if err := ValidateReviewChangeRequest(bad); err == nil {
				t.Fatal("invalid management change accepted")
			}
		})
	}
	apply := &placementpb.ApplyChangeRequest{Change: review, IdempotencyKey: "stable-command"}
	// A committed command can recover without its old token; the owner verifies the receipt.
	if err := ValidateApplyChangeRequest(apply); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*placementpb.ApplyChangeRequest){
		"missing_change": func(r *placementpb.ApplyChangeRequest) { r.Change = nil },
		"empty_key":      func(r *placementpb.ApplyChangeRequest) { r.IdempotencyKey = "" },
		"key_bound":      func(r *placementpb.ApplyChangeRequest) { r.IdempotencyKey = strings.Repeat("a", 129) },
		"token_bound":    func(r *placementpb.ApplyChangeRequest) { r.ReviewToken = strings.Repeat("a", 16385) },
		"ack_bound":      func(r *placementpb.ApplyChangeRequest) { r.AcknowledgedWarningIds = make([]string, 65) },
		"empty_ack":      func(r *placementpb.ApplyChangeRequest) { r.AcknowledgedWarningIds = []string{""} },
		"unknown_fields": func(r *placementpb.ApplyChangeRequest) { r.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01}) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := proto.CloneOf(apply)
			mutate(bad)
			if err := ValidateApplyChangeRequest(bad); err == nil {
				t.Fatal("invalid apply accepted")
			}
		})
	}
}
