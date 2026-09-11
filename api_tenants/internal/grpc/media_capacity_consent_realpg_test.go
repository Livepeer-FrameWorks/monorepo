//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestCapacityConsentManagement_RealPG(t *testing.T) {
	name := fmt.Sprintf("fw-consent-management-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start PostgreSQL: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	verifyCapacityConsentManagement(t, db)
}

func TestCapacityConsentManagement_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "quartermaster_consent_management")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyCapacityConsentManagement(t, db)
}

func verifyCapacityConsentManagement(t *testing.T, db *sql.DB) {
	t.Helper()
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	const tenant = "11111111-1111-4111-8111-111111111171"
	const other = "11111111-1111-4111-8111-111111111172"
	ctx := consentActor(tenant, "actor-owner", "owner")
	if _, err := db.Exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Consent owner'), ($2::uuid, 'Other owner')`, tenant, other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, owner_tenant_id) VALUES ('consent-managed', 'Managed', 'edge', 'https://managed.example', $1::uuid)`, tenant); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &QuartermasterServer{db: db, consentReviewKeyID: "review-one", consentReviewPrivateKey: key}
	get := &quartermasterpb.GetClusterMediaConsentRequest{ClusterId: "consent-managed"}
	state, err := s.GetClusterMediaConsent(ctx, get)
	if err != nil || !state.GetCanManage() || state.GetConsent().GetRevision() != 0 || state.GetRollout().GetStatus() != placementpb.RolloutStatus_ROLLOUT_STATUS_NOT_CONFIGURED {
		t.Fatalf("initial state: %+v, %v", state, err)
	}
	if _, err := s.GetClusterMediaConsent(consentActor(other, "other-actor", "owner"), get); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-owner read: %v", err)
	}
	change := &quartermasterpb.ReviewClusterMediaConsentRequest{ClusterId: get.ClusterId, AllowIngest: true}
	review, err := s.ReviewClusterMediaConsentChange(ctx, change)
	if err != nil || len(review.GetWarnings()) != 2 {
		t.Fatalf("review: %+v, %v", review, err)
	}
	apply := &quartermasterpb.ApplyClusterMediaConsentRequest{Change: change, ReviewToken: review.GetReviewToken(), IdempotencyKey: "first-command"}
	if _, err := s.ApplyClusterMediaConsentChange(ctx, apply); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing acknowledgements: %v", err)
	}
	for _, warning := range review.GetWarnings() {
		apply.AcknowledgedWarningIds = append(apply.AcknowledgedWarningIds, warning.GetId())
	}
	base, err := quartermasterdb.NewMediaConsentStore(db).Read(ctx, quartermasterdb.MediaConsentScope{TenantID: tenant, ClusterID: get.ClusterId})
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := buildConsentReview(quartermasterdb.MediaConsentScope{TenantID: tenant, ClusterID: get.ClusterId}, "actor-owner", base, consentNext(base.ClusterRecordID, change))
	if err != nil {
		t.Fatal(err)
	}
	expired := proto.CloneOf(apply)
	expired.ReviewToken, err = placement.IssueReview(s.consentReviewKeyID, key, binding, time.Now().Add(-2*placement.ReviewLifetime))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyClusterMediaConsentChange(ctx, expired); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expired review: %v", err)
	}
	tampered := proto.CloneOf(apply)
	tampered.Change.AllowIngest = false
	if _, err := s.ApplyClusterMediaConsentChange(ctx, tampered); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("changed reviewed command: %v", err)
	}
	tampered = proto.CloneOf(apply)
	tampered.ReviewToken = "unsigned"
	if _, err := s.ApplyClusterMediaConsentChange(ctx, tampered); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unsigned command: %v", err)
	}
	first, err := s.ApplyClusterMediaConsentChange(ctx, apply)
	if err != nil || first.GetRevision() != 1 || first.GetRollout().GetStatus() != placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING {
		t.Fatalf("apply: %+v, %v", first, err)
	}
	s.consentReviewPrivateKey = nil
	apply.ReviewToken = ""
	recovered, err := s.ApplyClusterMediaConsentChange(ctx, apply)
	if err != nil || !proto.Equal(first, recovered) {
		t.Fatalf("signer-outage recovery: %+v, %v", recovered, err)
	}
	state, err = s.GetClusterMediaConsent(ctx, get)
	if err != nil || state.GetCanManage() || state.GetConsent().GetAllowServe() || state.GetConsent().GetAllowExternalSource() {
		t.Fatalf("signer-outage read: %+v, %v", state, err)
	}
	if _, err := s.ApplyClusterMediaConsentChange(consentActor(tenant, "other-actor", "owner"), apply); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("actor substitution: %v", err)
	}
	if _, err := s.ApplyClusterMediaConsentChange(consentActor(tenant, "actor-owner", "viewer"), apply); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked role recovery: %v", err)
	}
	secondChange := &quartermasterpb.ReviewClusterMediaConsentRequest{ClusterId: get.ClusterId, ExpectedRevision: 1, AllowIngest: true, AllowServe: true}
	if _, err := s.ReviewClusterMediaConsentChange(ctx, secondChange); status.Code(err) != codes.Unavailable {
		t.Fatalf("unsigned new review: %v", err)
	}
	s.consentReviewPrivateKey = key
	secondReview, err := s.ReviewClusterMediaConsentChange(ctx, secondChange)
	if err != nil {
		t.Fatal(err)
	}
	secondApply := &quartermasterpb.ApplyClusterMediaConsentRequest{Change: secondChange, ReviewToken: secondReview.GetReviewToken(), IdempotencyKey: "concurrent-command"}
	for _, warning := range secondReview.GetWarnings() {
		secondApply.AcknowledgedWarningIds = append(secondApply.AcknowledgedWarningIds, warning.GetId())
	}
	var wg sync.WaitGroup
	responses := make(chan *quartermasterpb.ClusterMediaConsentChange, 8)
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := s.ApplyClusterMediaConsentChange(ctx, proto.CloneOf(secondApply))
			responses <- response
			errors <- err
		}()
	}
	wg.Wait()
	close(responses)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent same-command apply: %v", err)
		}
	}
	var expected *quartermasterpb.ClusterMediaConsentChange
	for response := range responses {
		if expected == nil {
			expected = response
		}
		if !proto.Equal(expected, response) || response.GetRevision() != 2 {
			t.Fatalf("divergent concurrent receipt: %+v", response)
		}
	}
	history, err := s.GetClusterMediaConsentChange(ctx, &quartermasterpb.GetClusterMediaConsentChangeRequest{ClusterId: get.ClusterId, IdempotencyKey: apply.IdempotencyKey})
	if err != nil || !proto.Equal(first, history) {
		t.Fatalf("immutable response: %+v, %v", history, err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE quartermaster.infrastructure_clusters SET owner_tenant_id = $1::uuid WHERE owner_tenant_id = $2::uuid AND cluster_id = $3`, other, tenant, get.ClusterId); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyClusterMediaConsentChange(ctx, secondApply); status.Code(err) != codes.NotFound {
		t.Fatalf("former owner recovery: %v", err)
	}
}
