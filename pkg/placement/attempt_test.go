package placement

import (
	"bytes"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPreparationAttemptIssuanceAndCanonicalIdentity(t *testing.T) {
	now := time.Unix(1800000000, 123456789)
	id, err := NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := PreparationIssuedAt(id)
	if err != nil || !issued.Equal(now.Truncate(time.Millisecond)) {
		t.Fatalf("issuance time changed: %v, %v", issued, err)
	}
	for _, invalid := range []string{"", "10000000-0000-4000-8000-000000000001", "not-uuid"} {
		if _, parseErr := PreparationIssuedAt(invalid); parseErr == nil {
			t.Fatalf("unbounded attempt accepted: %q", invalid)
		}
	}
	if _, issuanceErr := NewPreparationAttemptID(time.Time{}); issuanceErr == nil {
		t.Fatal("missing issuance clock accepted")
	}
	req := preparationWireFixture(t, now)
	req.Query.ClusterIds = []string{"us", "eu"}
	first, encoded, err := PreparationIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	reordered := proto.CloneOf(req)
	reordered.Query.ClusterIds = []string{"eu", "us"}
	second, other, err := PreparationIdentity(reordered)
	if err != nil || first != second || !bytes.Equal(encoded, other) || req.Query.ClusterIds[0] != "us" {
		t.Fatal("set order changed identity or canonicalization mutated caller")
	}
	reordered.ExpiresAt = timestamppb.New(req.ExpiresAt.AsTime().Add(time.Second))
	changed, _, err := PreparationIdentity(reordered)
	if err != nil || changed == first {
		t.Fatal("renewed decision preserved idempotency identity")
	}
	// Once an ID ages out, changing only its deadline cannot make it usable.
	late := now.Add(PreparationLifetime)
	reordered.ExpiresAt = timestamppb.New(late.Add(time.Second))
	if err := ValidatePreparationDeadline(reordered, late); err == nil {
		t.Fatal("expired attempt was reissued after receipt retention")
	}
}

func TestPreparationAttemptLifetimeBoundaries(t *testing.T) {
	issued := time.Unix(1800000000, 123000000).UTC()
	req := preparationWireFixture(t, issued)
	req.ExpiresAt = timestamppb.New(issued.Add(PreparationLifetime))
	for name, at := range map[string]time.Time{
		"issuance":                issued,
		"at_maximum_clock_skew":   issued.Add(-PreparationClockSkew),
		"inside_last_millisecond": issued.Add(PreparationLifetime - time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePreparationDeadline(req, at); err != nil {
				t.Fatalf("valid bounded attempt rejected: %v", err)
			}
		})
	}
	for name, at := range map[string]time.Time{
		"before_clock_skew_allowance": issued.Add(-PreparationClockSkew - time.Nanosecond),
		"exact_expiry":                issued.Add(PreparationLifetime),
		"after_expiry":                issued.Add(PreparationLifetime + time.Nanosecond),
		"missing_clock":               {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePreparationDeadline(req, at); err == nil {
				t.Fatal("attempt outside validity window accepted")
			}
		})
	}
	req.ExpiresAt = timestamppb.New(issued.Add(PreparationLifetime + time.Nanosecond))
	if err := ValidatePreparationDeadline(req, issued.Add(time.Second)); err == nil {
		t.Fatal("nanosecond extension beyond issuance horizon accepted")
	}
}
