//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMediaCapacityConsent_RealPG(t *testing.T) {
	verifyMediaCapacityConsent(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestMediaCapacityConsent_RealYugabyte(t *testing.T) {
	verifyMediaCapacityConsent(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

func verifyMediaCapacityConsent(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const owner = "11111111-1111-4111-8111-111111111151"
	const consumer = "11111111-1111-4111-8111-111111111152"
	const stranger = "11111111-1111-4111-8111-111111111153"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Consent owner'), ($2::uuid, 'Consent consumer'), ($3::uuid, 'Stranger')`, owner, consumer, stranger)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, owner_tenant_id)
		VALUES ('consent-owned', 'Owned', 'edge', 'https://owned.example', $1::uuid),
		('consent-recreated', 'Recreated', 'edge', 'https://recreated.example', $1::uuid),
		('consent-official', 'Official', 'edge', 'https://official.example', NULL)`, owner)
	exec(`INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_source, subscription_status, is_active)
		VALUES ($1::uuid, 'consent-owned', 'owner', 'active', true),
		($2::uuid, 'consent-owned', 'operator_override', 'active', true),
		($3::uuid, 'consent-owned', 'unknown', 'active', true)`, owner, consumer, stranger)
	store := NewMediaConsentStore(db)
	scope := MediaConsentScope{owner, "consent-owned"}
	before, err := store.Read(ctx, scope)
	if err != nil || before.Revision != 0 || !before.AllowIngest || !before.AllowServe || !before.AllowExternalSource || before.ClusterRecordID == "" {
		t.Fatalf("compatibility default: %+v, %v", before, err)
	}
	for _, other := range []MediaConsentScope{{consumer, scope.ClusterID}, {stranger, scope.ClusterID}, {owner, "missing"}, {owner, "consent-official"}} {
		if _, err := store.Read(ctx, other); !errors.Is(err, ErrConsentNotFound) {
			t.Fatalf("owner boundary: %+v: %v", other, err)
		}
	}
	countRefresh := func(tenant string) int {
		t.Helper()
		var count int
		// Changes of one (tenant, reason) fold into one unfinished row, so the
		// number of requested refreshes is the revision total, not the row count.
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(revision), 0) FROM quartermaster.media_authority_refresh_outbox WHERE tenant_id = $1::uuid`, tenant).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	counts := map[string]int{owner: countRefresh(owner), consumer: countRefresh(consumer), stranger: countRefresh(stranger)}
	projection, err := New(db).ListTenantEffectiveAccess(ctx, consumer)
	if err != nil || len(projection) != 1 || projection[0].RegionID != "" {
		t.Fatalf("unspecified cluster region projection: %+v, %v", projection, err)
	}
	exec(`UPDATE quartermaster.infrastructure_clusters SET region_id = 'us-east' WHERE cluster_id = $1 AND owner_tenant_id = $2::uuid`, scope.ClusterID, owner)
	projection, err = New(db).ListTenantEffectiveAccess(ctx, consumer)
	if err != nil || len(projection) != 1 || projection[0].RegionID != "us-east" {
		t.Fatalf("authorized region projection: %+v, %v", projection, err)
	}
	for tenant, count := range counts {
		if got := countRefresh(tenant); got != count+1 {
			t.Fatalf("region change omitted subscriber authority refresh for %s: %d, want %d", tenant, got, count+1)
		}
		counts[tenant] = count + 1
	}
	input := MediaConsentApply{Scope: scope, Consent: before, ActorID: "actor-owner", IdempotencyKey: "consent-first", ReviewDigest: strings.Repeat("a", 64), AcknowledgedWarnings: []string{"existing-sessions", "capacity-unavailable"}}
	input.Consent.Revision, input.Consent.AllowServe, input.Consent.AllowExternalSource = 1, false, false
	validate := func(got MediaConsent) error {
		if got != before {
			return fmt.Errorf("review did not receive locked base: %+v", got)
		}
		return nil
	}
	receipt, err := store.Apply(ctx, input, validate)
	if err != nil || receipt.Revision != 1 || receipt.ClusterRecordID != before.ClusterRecordID || receipt.AllowServe || receipt.AllowExternalSource || !receipt.PreviousAllowServe || !receipt.PreviousAllowExternalSource {
		t.Fatalf("apply: %+v, %v", receipt, err)
	}
	for tenant, count := range counts {
		if got := countRefresh(tenant); got != count+1 {
			t.Fatalf("subscriber refresh for %s: %d, want %d", tenant, got, count+1)
		}
	}
	if got, err := store.Read(ctx, scope); err != nil || got != input.Consent {
		t.Fatalf("persisted consent: %+v, %v", got, err)
	}
	projection, err = New(db).ListTenantEffectiveAccess(ctx, consumer)
	if err != nil || len(projection) != 1 || projection[0].MediaAllowServe || projection[0].MediaConsentRevision != 1 || projection[0].OwnerTenantID != owner {
		t.Fatalf("consumer projection: %+v, %v", projection, err)
	}
	projection, err = New(db).ListTenantEffectiveAccess(ctx, stranger)
	if err != nil || len(projection) != 0 {
		t.Fatalf("consent must not grant entitlement: %+v, %v", projection, err)
	}
	input.AcknowledgedWarnings = []string{"capacity-unavailable", "existing-sessions", "capacity-unavailable"}
	retry, err := store.Apply(ctx, input, func(MediaConsent) error { return errors.New("expired review must not invalidate committed recovery") })
	if err != nil || !reflect.DeepEqual(receipt, retry) {
		t.Fatalf("exact retry: %+v, %v", retry, err)
	}
	for tenant, count := range counts {
		if got := countRefresh(tenant); got != count+1 {
			t.Fatalf("retry emitted refresh for %s: %d", tenant, got)
		}
	}
	for name, mutate := range map[string]func(*MediaConsentApply){
		"actor":            func(v *MediaConsentApply) { v.ActorID = "another-actor" },
		"payload":          func(v *MediaConsentApply) { v.Consent.AllowServe = true },
		"review":           func(v *MediaConsentApply) { v.ReviewDigest = strings.Repeat("b", 64) },
		"acknowledgements": func(v *MediaConsentApply) { v.AcknowledgedWarnings = []string{"different"} },
	} {
		changed := input
		mutate(&changed)
		if _, err := store.Apply(ctx, changed, validate); !errors.Is(err, ErrConsentIdempotencyConflict) {
			t.Fatalf("%s retry mismatch: %v", name, err)
		}
	}
	stale := input
	stale.IdempotencyKey = "stale-new-key"
	if _, err := store.Apply(ctx, stale, validate); !errors.Is(err, ErrConsentRevisionConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	failed := input
	failed.ExpectedRevision, failed.Consent.Revision, failed.IdempotencyKey = 1, 2, "failed-review"
	reviewErr := errors.New("review revoked")
	if _, err := store.Apply(ctx, failed, func(MediaConsent) error { return reviewErr }); !errors.Is(err, reviewErr) {
		t.Fatalf("rejected review: %v", err)
	}
	if _, err := store.Change(ctx, scope, failed.IdempotencyKey); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("rejected review left receipt: %v", err)
	}
	if got, err := store.Read(ctx, scope); err != nil || got != input.Consent {
		t.Fatalf("review failure changed state: %+v, %v", got, err)
	}

	// A receipt failure happens after the consent UPDATE and its refresh trigger.
	exec(`ALTER TABLE quartermaster.media_capacity_consent_changes ADD CONSTRAINT test_consent_receipt_failure CHECK (actor_id <> 'reject-receipt')`)
	failed.ActorID = "reject-receipt"
	if _, err := store.Apply(ctx, failed, func(MediaConsent) error { return nil }); err == nil {
		t.Fatal("receipt constraint unexpectedly accepted")
	}
	exec(`ALTER TABLE quartermaster.media_capacity_consent_changes DROP CONSTRAINT test_consent_receipt_failure`)
	if got, err := store.Read(ctx, scope); err != nil || got != input.Consent {
		t.Fatalf("receipt failure changed consent: %+v, %v", got, err)
	}
	for tenant, count := range counts {
		if got := countRefresh(tenant); got != count+1 {
			t.Fatalf("rolled back transaction emitted refresh for %s: %d", tenant, got)
		}
	}

	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			concurrent := input
			concurrent.ExpectedRevision, concurrent.Consent.Revision, concurrent.IdempotencyKey = 1, 2, fmt.Sprintf("concurrent-%d", i)
			_, err := store.Apply(ctx, concurrent, func(MediaConsent) error { return nil })
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrConsentRevisionConflict) {
			t.Fatalf("concurrent apply: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners: %d", winners)
	}
	if historical, err := store.Change(ctx, scope, input.IdempotencyKey); err != nil || !reflect.DeepEqual(historical, receipt) {
		t.Fatalf("immutable history: %+v, %v", historical, err)
	}
	exec(`UPDATE quartermaster.infrastructure_clusters SET owner_tenant_id = $1::uuid WHERE cluster_id = $2 AND owner_tenant_id = $3::uuid`, consumer, scope.ClusterID, owner)
	if _, err := store.Apply(ctx, input, validate); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("former owner replay: %v", err)
	}
	for _, tenant := range []string{owner, consumer} {
		if _, err := store.Change(ctx, MediaConsentScope{tenant, scope.ClusterID}, input.IdempotencyKey); !errors.Is(err, ErrConsentNotFound) {
			t.Fatalf("transferred receipt disclosure for %s: %v", tenant, err)
		}
	}
	newScope := MediaConsentScope{owner, "consent-recreated"}
	original, err := store.Read(ctx, newScope)
	if err != nil {
		t.Fatal(err)
	}
	recreated := input
	recreated.Scope, recreated.Consent, recreated.IdempotencyKey = newScope, original, "recreated-command"
	recreated.Consent.Revision = 1
	if _, err := store.Apply(ctx, recreated, func(MediaConsent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $1::uuid AND cluster_id = $2`, owner, newScope.ClusterID)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, owner_tenant_id) VALUES ($1, 'Replacement', 'edge', 'https://replacement.example', $2::uuid)`, newScope.ClusterID, owner)
	if _, err := store.Change(ctx, newScope, recreated.IdempotencyKey); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("recreated cluster receipt disclosure: %v", err)
	}
	if _, err := store.Apply(ctx, recreated, func(MediaConsent) error { return nil }); !errors.Is(err, ErrConsentRevisionConflict) {
		t.Fatalf("recreated cluster replay: %v", err)
	}
	replacement, err := store.Read(ctx, newScope)
	if err != nil || replacement.Revision != 0 || replacement.ClusterRecordID == original.ClusterRecordID {
		t.Fatalf("replacement state: %+v, %v", replacement, err)
	}
	recreated.Consent = replacement
	recreated.Consent.Revision = 1
	if _, err := store.Apply(ctx, recreated, func(MediaConsent) error { return nil }); err != nil {
		t.Fatalf("fresh replacement command: %v", err)
	}
}
