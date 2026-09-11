package quartermasterdb

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestMediaConsentInvalidInputDoesNotTouchDatabase(t *testing.T) {
	store := NewMediaConsentStore(nil)
	valid := MediaConsentApply{
		Scope:   MediaConsentScope{"11111111-1111-4111-8111-111111111151", "owned"},
		Consent: MediaConsent{"11111111-1111-4111-8111-111111111161", 1, true, false, true},
		ActorID: "actor", IdempotencyKey: "key", ReviewDigest: strings.Repeat("a", 64),
	}
	for name, mutate := range map[string]func(*MediaConsentApply){
		"zero tenant":         func(v *MediaConsentApply) { v.Scope.TenantID = "00000000-0000-0000-0000-000000000000" },
		"noncanonical tenant": func(v *MediaConsentApply) { v.Scope.TenantID = "{11111111-1111-4111-8111-111111111151}" },
		"empty cluster":       func(v *MediaConsentApply) { v.Scope.ClusterID = "" },
		"control cluster":     func(v *MediaConsentApply) { v.Scope.ClusterID = "a\nb" },
		"oversized cluster":   func(v *MediaConsentApply) { v.Scope.ClusterID = strings.Repeat("a", 101) },
		"empty record":        func(v *MediaConsentApply) { v.Consent.ClusterRecordID = "" },
		"actor":               func(v *MediaConsentApply) { v.ActorID = " " },
		"key":                 func(v *MediaConsentApply) { v.IdempotencyKey = "key\x00" },
		"review":              func(v *MediaConsentApply) { v.ReviewDigest = strings.Repeat("A", 64) },
		"revision":            func(v *MediaConsentApply) { v.ExpectedRevision = 1 },
		"overflow": func(v *MediaConsentApply) {
			v.ExpectedRevision = math.MaxInt64
			v.Consent.Revision = uint64(math.MaxInt64) + 1
		},
		"unknown base":       func(v *MediaConsentApply) { v.ExpectedRevision = math.MaxUint64; v.Consent.Revision = 0 },
		"warning":            func(v *MediaConsentApply) { v.AcknowledgedWarnings = []string{"\t"} },
		"unbounded warnings": func(v *MediaConsentApply) { v.AcknowledgedWarnings = make([]string, 129) },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if _, err := store.Apply(context.Background(), input, func(MediaConsent) error { return nil }); !errors.Is(err, ErrConsentInvalidInput) {
				t.Fatalf("invalid input accepted: %v", err)
			}
		})
	}
	if _, err := store.Apply(context.Background(), valid, nil); !errors.Is(err, ErrConsentInvalidInput) {
		t.Fatalf("missing review validator: %v", err)
	}
}
