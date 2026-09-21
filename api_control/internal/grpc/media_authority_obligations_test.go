package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	settleComplete = `(?s)UPDATE commodore\.media_authority_refresh_obligations\s+SET status = 'completed'`
	settleFail     = `(?s)UPDATE commodore\.media_authority_refresh_obligations\s+SET status = 'pending', next_attempt_at`
	settlePark     = `(?s)UPDATE commodore\.media_authority_refresh_obligations\s+SET status = 'parked'`
	settleRelease  = `(?s)UPDATE commodore\.media_authority_refresh_obligations\s+SET lease_expires_at = NULL`
)

func TestInvalidObjectPlacementParentIsPermanent(t *testing.T) {
	s := &CommodoreServer{}
	err := s.compileObjectPlacement(context.Background(), &mediapb.TenantAuthority{SchemaVersion: 99}, &mediapb.MediaObjectAuthority{})
	if class, reason := classifyAuthorityCompileError(err); class != authorityCompilePark || reason != "invalid_placement_parent" {
		t.Fatalf("invalid placement looked like a dependency outage: %v (%v, %s)", err, class, reason)
	}
}

// The churn alert counts renewals of versions that had used less than a quarter
// of their validity. Renewal is due at a third, so the count is independent of
// how many authorities exist and of how long each is valid: a 24-hour tenant
// and a 30-second quote renewed on schedule are both not early.
func TestEarlyRenewalIsMeasuredAgainstEachVersionsOwnValidity(t *testing.T) {
	now := time.Now().UTC()
	decision := func(issuedAgo, lifetime time.Duration, cause string) mediaAuthorityPublication {
		issued := now.Add(-issuedAgo)
		return mediaAuthorityPublication{publish: true, cause: cause, hasCurrent: true,
			current: commodoredb.GetCurrentMediaAuthorityPublicationRow{IssuedAt: issued, ValidUntil: issued.Add(lifetime)}}
	}
	for name, tc := range map[string]struct {
		decision mediaAuthorityPublication
		early    bool
	}{
		"a tenant renewed at a third":         {decision(8*time.Hour, 24*time.Hour, mediaAuthorityCauseRenewal), false},
		"a quote renewed at half":             {decision(15*time.Second, 30*time.Second, mediaAuthorityCauseRenewal), false},
		"a tenant renewed an hour in":         {decision(time.Hour, 24*time.Hour, mediaAuthorityCauseRenewal), true},
		"a quote renewed five seconds in":     {decision(5*time.Second, 30*time.Second, mediaAuthorityCauseRenewal), true},
		"a content change is never a renewal": {decision(time.Minute, 24*time.Hour, mediaAuthorityCauseContent), false},
	} {
		if got := tc.decision.renewedEarly(now); got != tc.early {
			t.Errorf("%s: early = %v, want %v", name, got, tc.early)
		}
	}
}

// What an unchanged recompile decides, for the cases where deciding wrongly
// publishes a version on every compile or lets a terminal authority renew.
func TestDecideMediaAuthorityPublicationForUnchangedContent(t *testing.T) {
	digest := []byte("same-content")
	target := commodoredb.TenantMediaAuthorityTarget("tenant-1")
	now := time.Now().UTC()
	issuedAt := now.Add(-mediaAuthorityValidity / 2)
	for name, tc := range map[string]struct {
		currentCells []string
		targets      []string
		tombstone    bool
		// olderCopyHeld: a cell may still hold a valid version from before the
		// tombstone, valid past the tombstone itself.
		olderCopyHeld bool
		wantCause     string
	}{
		// The database orders cell ids by its collation ("cell-a" before
		// "cell-B"); Go orders bytes ("cell-B" first). The same set must compare
		// equal whichever order either side arrives in.
		"the same cells in database collation order are not a target change": {
			currentCells: []string{"cell-a", "cell-B"}, targets: []string{"cell-B", "cell-a"}, wantCause: mediaAuthorityCauseRenewal,
		},
		"a different cell set publishes": {
			currentCells: []string{"cell-a"}, targets: []string{"cell-a", "cell-B"}, wantCause: mediaAuthorityCauseTargets,
		},
		"a tombstone no cell can hold stale is not renewed": {
			currentCells: []string{"cell-a"}, targets: []string{"cell-a"}, tombstone: true,
		},
		"a tombstone is renewed while a cell may hold an older copy": {
			currentCells: []string{"cell-a"}, targets: []string{"cell-a"}, tombstone: true, olderCopyHeld: true, wantCause: mediaAuthorityCauseRenewal,
		},
		"a tombstone still follows its target cells": {
			currentCells: []string{"cell-a"}, targets: []string{"cell-a", "cell-B"}, tombstone: true, wantCause: mediaAuthorityCauseTargets,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			mock.ExpectQuery(`(?s)SELECT versions\.authority_version, versions\.content_digest`).WithArgs("tenant", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"authority_version", "content_digest", "dependents_digest", "issued_at", "refresh_after", "valid_until"}).
					AddRow(int64(4), digest, nil, issuedAt, issuedAt.Add(mediaAuthorityValidity/2), issuedAt.Add(mediaAuthorityValidity)))
			if tc.tombstone {
				horizon := issuedAt.Add(mediaAuthorityValidity)
				if tc.olderCopyHeld {
					horizon = now.Add(30 * 24 * time.Hour)
				}
				mock.ExpectQuery("GetMediaAuthorityValidHorizon").WithArgs("tenant", "tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"valid_until"}).AddRow(horizon))
			}
			cells := sqlmock.NewRows([]string{"cell_id"})
			for _, cell := range tc.currentCells {
				cells.AddRow(cell)
			}
			mock.ExpectQuery("ListCurrentMediaAuthorityDeliveryCells").WithArgs("tenant", "tenant-1").WillReturnRows(cells)
			decision, err := decideMediaAuthorityPublication(context.Background(), commodoredb.New(server.db), "tenant", "tenant-1", target, digest, tc.targets, tc.tombstone, now.Add(mediaAuthorityValidity), now)
			if err != nil {
				t.Fatal(err)
			}
			if decision.cause != tc.wantCause || decision.publish != (tc.wantCause != "") {
				t.Fatalf("decision = publish %v cause %q, want cause %q", decision.publish, decision.cause, tc.wantCause)
			}
		})
	}
}

// A signed authority carries the stream's process configuration, and the
// publication digest covers it. An authority compiled while Purser is
// unreachable must fail and retry: publishing the empty fallback would strip
// the tenant's transcoding, and nothing would correct it until the next renewal.
func TestAuthorityProcessConfigFailsClosedWhenPurserIsUnreachable(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "purser down")
	for name, fake := range map[string]*purserEntitlementFake{
		"subscription lookup fails": {subscription: func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
			return nil, unavailable
		}},
		"tier lookup fails": {
			subscription: func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
				return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-1"}}, nil
			},
			tier: func(context.Context, *purserpb.GetBillingTierRequest) (*purserpb.BillingTier, error) {
				return nil, unavailable
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, _, done := newMockServer(t)
			defer done()
			server.purserClient = startPurserEntitlementFake(t, fake)
			processes, err := server.resolveProcessesJSONStrict(context.Background(), "tenant-1", "stream-1", "cluster-1", "live")
			if err == nil {
				t.Fatalf("compiled process config %q while Purser was unreachable", processes)
			}
			if class, _ := classifyAuthorityCompileError(err); class != authorityCompileTransient {
				t.Fatalf("a Purser outage classified %v, want a retried transient failure", class)
			}
		})
	}
}

// A tenant's objects claimed together share one read of what they all derive
// from. The next claim reads again: a memo that outlived its claim could hand a
// compile the state from before the change that enqueued it, and an unchanged
// compile publishes nothing, so that would never be corrected.
func TestCompileInputsAreSharedWithinAClaimAndNeverBeyondIt(t *testing.T) {
	loads := 0
	load := func() (string, error) { loads++; return fmt.Sprintf("tier-%d", loads), nil }

	claim := withMediaAuthorityCompileMemo(context.Background(), newMediaAuthorityCompileMemo())
	for range 3 {
		if got, err := memoizedCompileInput(claim, "billing_tier\x00t1", load); err != nil || got != "tier-1" {
			t.Fatalf("within one claim: %q (err %v), want the first read shared", got, err)
		}
	}
	nextClaim := withMediaAuthorityCompileMemo(context.Background(), newMediaAuthorityCompileMemo())
	if got, _ := memoizedCompileInput(nextClaim, "billing_tier\x00t1", load); got != "tier-2" {
		t.Fatalf("the next claim reused %q from the one before it", got)
	}
	if got, _ := memoizedCompileInput(context.Background(), "billing_tier\x00t1", load); got != "tier-3" {
		t.Fatalf("a compile outside a claim got %q, want its own read", got)
	}

	failures := 0
	failing := func() (string, error) { failures++; return "", errors.New("purser down") }
	for range 2 {
		if _, err := memoizedCompileInput(claim, "subscription\x00t1", failing); err == nil {
			t.Fatal("a failed read was served as a value")
		}
	}
	if failures != 2 {
		t.Fatalf("a failed read was kept: %d loads for two compiles", failures)
	}
}

// How a claimed obligation settles decides whether a broken target costs one
// attempt or one attempt per backoff period forever.
func TestSettleMediaAuthorityObligation(t *testing.T) {
	row := func(attempts int32) commodoredb.ClaimMediaAuthorityObligationsRow {
		return commodoredb.ClaimMediaAuthorityObligationsRow{
			TargetKey: "media_object:live_stream:stream-1", Lane: commodoredb.MediaAuthorityLaneEvent,
			TenantID: "tenant-1", TargetKind: commodoredb.MediaAuthorityTargetLiveStream, Revision: 7, Attempts: attempts, ClaimToken: "claim-1",
		}
	}
	// Every settlement and release carries the claim's token: a worker whose lease
	// lapsed must not settle the attempt that replaced it.
	key, lane, revision, token := "media_object:live_stream:stream-1", commodoredb.MediaAuthorityLaneEvent, int64(7), "claim-1"

	renewalLane := commodoredb.MediaAuthorityLaneObjectDeadline

	for name, tc := range map[string]struct {
		attempts   int32
		lane       string
		compileErr error
		expect     func(sqlmock.Sqlmock)
	}{
		// The winner of the fence may have published nothing and left this renewal
		// row as the authority's only schedule. Completing it would let the
		// authority run to hard expiry with nothing left to renew it.
		"a renewal that loses the compile fence stays scheduled": {
			attempts:   1,
			lane:       renewalLane,
			compileErr: fmt.Errorf("persist: %w", errMediaAuthorityCompileSuperseded),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settleFail).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), key, renewalLane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		"success completes at the claimed revision": {
			attempts: 1,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settleComplete).WithArgs(key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		"an event folded in during the compile leaves the row pending": {
			attempts: 1,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settleComplete).WithArgs(key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec(settleRelease).WithArgs(key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		// The compile that overtook this event can still fail; nothing else would
		// compile the change the event carries.
		"an event that loses the compile fence stays pending": {
			attempts:   1,
			compileErr: fmt.Errorf("persist: %w", errMediaAuthorityCompileSuperseded),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settleFail).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		"a failure retrying cannot fix parks on the first attempt": {
			attempts:   1,
			compileErr: fmt.Errorf("publish: %w", sharedauthority.ErrMalformed),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settlePark).WithArgs("malformed_authority", sqlmock.AnyArg(), key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		"a transient failure backs off": {
			attempts:   3,
			compileErr: errors.New("load tenant billing authority: unavailable"),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settleFail).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		"a transient failure that keeps failing parks as exhausted": {
			attempts:   mediaAuthorityMaxTransientAttempts,
			compileErr: errors.New("load tenant billing authority: unavailable"),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(settlePark).WithArgs("exhausted", sqlmock.AnyArg(), key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		// Parked first and the tenant requested in the same transaction: the
		// tenant compile, which wakes parked objects, can only start once the
		// park is visible.
		"an object without a tenant authority parks and requests it": {
			attempts:   1,
			compileErr: fmt.Errorf("load: %w", errTenantAuthorityMissing),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec("RecordMediaAuthorityUse").WithArgs("tenant", "tenant-1", "tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("ReviveDormantMediaAuthorityRenewal").WithArgs("tenant:tenant-1", "").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit()
				mock.ExpectBegin()
				mock.ExpectExec(settlePark).WithArgs("awaiting_tenant_authority", sqlmock.AnyArg(), key, lane, revision, token).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
					WithArgs("event", "tenant:tenant-1", "tenant", "tenant-1", "media_object_awaiting_tenant_authority", "commodore", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			tc.expect(mock)
			claimed := row(tc.attempts)
			if tc.lane != "" {
				claimed.Lane = tc.lane
			}
			// Whatever the outcome, the lane's claim on the target is released so
			// another lane need not wait for its lease.
			mock.ExpectExec("ReleaseMediaAuthorityTargetClaim").WithArgs(key, claimed.Lane, token).WillReturnResult(sqlmock.NewResult(0, 1))
			server.settleMediaAuthorityObligation(claimed, &mediaAuthorityCompileOutcome{}, tc.compileErr)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
