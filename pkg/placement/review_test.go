package placement

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReviewBindsExactChangeAndExpires(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	binding := ReviewBinding{TenantID: "tenant-a", ActorID: "user-a", ScopeKind: "stream", ScopeID: "stream-a", ExpectedRevision: 3, ExpectedParentRevision: 8, PolicyDigest: strings.Repeat("a", 64), ContextDigest: strings.Repeat("b", 64), RequiredWarnings: []string{"paid-fallback", "availability"}}
	token, err := IssueReview("key-1", private, binding, now)
	if err != nil {
		t.Fatal(err)
	}
	trust := map[string]ed25519.PublicKey{"key-1": public}
	recovered, recoverErr := SignedReviewBinding(token, trust)
	if recoverErr != nil {
		t.Fatal(recoverErr)
	}
	recoveredDigest, recoverErr := ReviewDigest(recovered)
	expectedDigest, _ := ReviewDigest(binding)
	if recoverErr != nil || recoveredDigest != expectedDigest {
		t.Fatal("original committed intent could not be reconstructed")
	}
	if _, untrustedErr := SignedReviewBinding(token, nil); !errors.Is(untrustedErr, ErrStaleReview) {
		t.Fatal("untrusted review accepted for receipt reconstruction")
	}
	if verifyErr := VerifyReview(token, trust, binding, now.Add(time.Minute)); verifyErr != nil {
		t.Fatal(verifyErr)
	}
	for name, change := range map[string]func(*ReviewBinding){
		"tenant":                     func(b *ReviewBinding) { b.TenantID = "tenant-b" },
		"actor":                      func(b *ReviewBinding) { b.ActorID = "user-b" },
		"scope":                      func(b *ReviewBinding) { b.ScopeID = "stream-b" },
		"owner consent":              func(b *ReviewBinding) { b.ScopeKind = "cluster_consent"; b.ExpectedParentRevision = 0 },
		"revision":                   func(b *ReviewBinding) { b.ExpectedRevision++ },
		"parent":                     func(b *ReviewBinding) { b.ExpectedParentRevision++ },
		"rules":                      func(b *ReviewBinding) { b.PolicyDigest = strings.Repeat("c", 64) },
		"commercial or impact facts": func(b *ReviewBinding) { b.ContextDigest = strings.Repeat("d", 64) },
		"warnings":                   func(b *ReviewBinding) { b.RequiredWarnings = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := binding
			change(&changed)
			if verifyErr := VerifyReview(token, trust, changed, now); !errors.Is(verifyErr, ErrStaleReview) {
				t.Fatalf("substituted review accepted: %v", verifyErr)
			}
		})
	}
	for _, instant := range []time.Time{now.Add(-6 * time.Second), now.Add(5 * time.Minute), now.Add(time.Hour)} {
		if verifyErr := VerifyReview(token, trust, binding, instant); !errors.Is(verifyErr, ErrStaleReview) {
			t.Fatalf("invalid time accepted: %v", verifyErr)
		}
	}
	if verifyErr := VerifyReview(token, nil, binding, now); !errors.Is(verifyErr, ErrStaleReview) {
		t.Fatal("unknown signing key accepted")
	}
	if ackErr := ValidateReviewAcknowledgements(binding, []string{"availability", "paid-fallback"}); ackErr != nil {
		t.Fatal(ackErr)
	}
	for _, incomplete := range [][]string{nil, {"paid-fallback"}, {"availability", "paid-fallback", "invented"}} {
		if ackErr := ValidateReviewAcknowledgements(binding, incomplete); ackErr == nil {
			t.Fatal("incomplete or invented acknowledgements accepted")
		}
	}
	before, err := ReviewDigest(binding)
	if err != nil {
		t.Fatal(err)
	}
	binding.RequiredWarnings = []string{"availability", "paid-fallback", "availability"}
	after, err := ReviewDigest(binding)
	if err != nil || before != after {
		t.Fatalf("warning order changed review identity: %v", err)
	}
}
