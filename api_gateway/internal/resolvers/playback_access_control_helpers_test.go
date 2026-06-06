package resolvers

import (
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Intent: pickPlaybackPolicyTarget enforces the schema's "exactly one of
// streamId/vodAssetId/clipId" rule. 0 set -> error, exactly 1 -> that target,
// >1 -> error. Plain (non-globalid) ids pass through normalization unchanged.
func TestPickPlaybackPolicyTarget(t *testing.T) {
	ptr := func(s string) *string { return &s }

	t.Run("stream only", func(t *testing.T) {
		got, verr := pickPlaybackPolicyTarget(model.SetPlaybackPolicyInput{StreamID: ptr("stream-1")})
		if verr != nil {
			t.Fatalf("unexpected ValidationError: %v", verr.Message)
		}
		if got.kind != "stream" || got.id != "stream-1" {
			t.Fatalf("target = %+v, want {stream stream-1}", got)
		}
	})

	t.Run("vod only", func(t *testing.T) {
		got, verr := pickPlaybackPolicyTarget(model.SetPlaybackPolicyInput{VodAssetID: ptr("vodhash")})
		if verr != nil || got.kind != "vod_asset" || got.id != "vodhash" {
			t.Fatalf("got (%+v,%v), want vod_asset/vodhash", got, verr)
		}
	})

	t.Run("clip only", func(t *testing.T) {
		got, verr := pickPlaybackPolicyTarget(model.SetPlaybackPolicyInput{ClipID: ptr("cliphash")})
		if verr != nil || got.kind != "clip" || got.id != "cliphash" {
			t.Fatalf("got (%+v,%v), want clip/cliphash", got, verr)
		}
	})

	t.Run("none set is rejected", func(t *testing.T) {
		_, verr := pickPlaybackPolicyTarget(model.SetPlaybackPolicyInput{})
		if verr == nil {
			t.Fatal("expected ValidationError when no target set")
		}
	})

	t.Run("blank string counts as unset", func(t *testing.T) {
		_, verr := pickPlaybackPolicyTarget(model.SetPlaybackPolicyInput{StreamID: ptr("   ")})
		if verr == nil {
			t.Fatal("expected ValidationError when only-whitespace target set")
		}
	})

	t.Run("more than one is rejected", func(t *testing.T) {
		_, verr := pickPlaybackPolicyTarget(model.SetPlaybackPolicyInput{StreamID: ptr("s"), ClipID: ptr("c")})
		if verr == nil {
			t.Fatal("expected ValidationError when two targets set")
		}
	})
}

// Intent: protoPolicyType <-> modelPolicyType round-trip every valid policy
// type, and both reject unknowns.
func TestPolicyTypeRoundTrip(t *testing.T) {
	for _, mt := range []model.PlaybackPolicyType{
		model.PlaybackPolicyTypePublic, model.PlaybackPolicyTypeJwt, model.PlaybackPolicyTypeWebhook,
	} {
		s, err := protoPolicyType(mt)
		if err != nil {
			t.Fatalf("protoPolicyType(%v) error: %v", mt, err)
		}
		back, ok := modelPolicyType(s)
		if !ok || back != mt {
			t.Fatalf("round-trip %v -> %q -> (%v,%v)", mt, s, back, ok)
		}
	}

	if _, err := protoPolicyType(model.PlaybackPolicyType("BOGUS")); err == nil {
		t.Fatal("protoPolicyType should reject unknown type")
	}
	if _, ok := modelPolicyType("nonsense"); ok {
		t.Fatal("modelPolicyType should reject unknown string")
	}
	// Proto strings are matched case-insensitively (Commodore may send "JWT").
	if got, ok := modelPolicyType("JWT"); !ok || got != model.PlaybackPolicyTypeJwt {
		t.Fatalf("modelPolicyType(JWT) = (%v,%v), want JWT type", got, ok)
	}
}

// Intent: canonicalJSONValue makes semantically-equal claim values compare
// equal byte-for-byte (so claim-requirement matching is order-independent),
// and rejects empty / malformed / multi-value input.
func TestCanonicalJSONValue(t *testing.T) {
	a, err := canonicalJSONValue(`{"a":1,"b":2}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := canonicalJSONValue(`{"b":2,"a":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a != b {
		t.Fatalf("key order should not change canonical form: %q vs %q", a, b)
	}

	for _, bad := range []string{"", "   ", "{not json", `1 2`, `{} {}`} {
		if _, err := canonicalJSONValue(bad); err == nil {
			t.Fatalf("canonicalJSONValue(%q) should error", bad)
		}
	}
}

// Intent: claimReqsToProto trims names, drops blank/nil entries, canonicalizes
// values, and rejects malformed JSON values with a field-scoped message.
func TestClaimReqsToProto(t *testing.T) {
	t.Run("empty in -> nil out", func(t *testing.T) {
		out, verr := claimReqsToProto(nil)
		if out != nil || verr != nil {
			t.Fatalf("got (%v,%v), want (nil,nil)", out, verr)
		}
	})

	t.Run("blank and nil entries dropped", func(t *testing.T) {
		in := []*model.PlaybackJwtClaimRequirementInput{
			nil,
			{Name: "  ", JSONValue: `1`},
			{Name: "plan", JSONValue: `"pro"`},
		}
		out, verr := claimReqsToProto(in)
		if verr != nil {
			t.Fatalf("unexpected ValidationError: %v", verr.Message)
		}
		if len(out) != 1 || out["plan"] != `"pro"` {
			t.Fatalf("out = %v, want only canonical plan claim", out)
		}
	})

	t.Run("malformed value rejected", func(t *testing.T) {
		in := []*model.PlaybackJwtClaimRequirementInput{{Name: "x", JSONValue: "{bad"}}
		_, verr := claimReqsToProto(in)
		if verr == nil {
			t.Fatal("expected ValidationError for malformed claim JSON")
		}
	})
}

// Intent: playbackWebhookSecretInput is the reflection guard for the optional
// webhook secret. Pin every branch: missing block, nil ptr secret, blank
// secret, non-string secret, and a valid trimmed secret.
func TestPlaybackWebhookSecretInput(t *testing.T) {
	type ptrSecret struct{ Secret *string }
	type strSecret struct{ Secret string }
	type intSecret struct{ Secret *int }
	type noSecret struct{ Other string }
	sp := func(s string) *string { return &s }

	t.Run("nil interface -> block required", func(t *testing.T) {
		if _, verr := playbackWebhookSecretInput(nil); verr == nil {
			t.Fatal("nil webhook should require the block")
		}
	})

	t.Run("typed nil pointer -> block required", func(t *testing.T) {
		var p *ptrSecret
		if _, verr := playbackWebhookSecretInput(p); verr == nil {
			t.Fatal("nil pointer webhook should require the block")
		}
	})

	t.Run("missing secret field", func(t *testing.T) {
		if _, verr := playbackWebhookSecretInput(noSecret{}); verr == nil {
			t.Fatal("struct without Secret should error")
		}
	})

	t.Run("nil secret pointer is allowed (no rotation)", func(t *testing.T) {
		got, verr := playbackWebhookSecretInput(&ptrSecret{Secret: nil})
		if verr != nil || got != "" {
			t.Fatalf("nil secret ptr -> (%q,%v), want (\"\",nil)", got, verr)
		}
	})

	t.Run("blank secret rejected", func(t *testing.T) {
		if _, verr := playbackWebhookSecretInput(&ptrSecret{Secret: sp("   ")}); verr == nil {
			t.Fatal("blank secret should be rejected")
		}
	})

	t.Run("valid secret is trimmed", func(t *testing.T) {
		got, verr := playbackWebhookSecretInput(&ptrSecret{Secret: sp("  s3cr3t  ")})
		if verr != nil || got != "s3cr3t" {
			t.Fatalf("got (%q,%v), want trimmed s3cr3t", got, verr)
		}
	})

	t.Run("string-kind secret field is trimmed", func(t *testing.T) {
		got, verr := playbackWebhookSecretInput(strSecret{Secret: "  abc "})
		if verr != nil || got != "abc" {
			t.Fatalf("got (%q,%v), want abc", got, verr)
		}
	})

	t.Run("non-string secret rejected", func(t *testing.T) {
		n := 5
		if _, verr := playbackWebhookSecretInput(&intSecret{Secret: &n}); verr == nil {
			t.Fatal("non-string secret should be rejected")
		}
	})
}

// Intent: ParseRFC3339OrNil accepts both nano and plain RFC3339, returns nil
// for blank or unparseable input (never a zero-time pointer).
func TestParseRFC3339OrNil(t *testing.T) {
	if got := ParseRFC3339OrNil("  "); got != nil {
		t.Fatalf("blank -> %v, want nil", got)
	}
	if got := ParseRFC3339OrNil("not-a-time"); got != nil {
		t.Fatalf("junk -> %v, want nil", got)
	}
	plain := ParseRFC3339OrNil("2026-06-06T10:00:00Z")
	if plain == nil || !plain.Equal(time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("plain RFC3339 parsed wrong: %v", plain)
	}
	if ParseRFC3339OrNil("2026-06-06T10:00:00.5Z") == nil {
		t.Fatal("nano RFC3339 should parse")
	}
}

// Intent: ModelSigningKeyStatus maps "revoked" (case-insensitive) to Revoked
// and everything else to Active (a missing/unknown status is treated as live).
func TestModelSigningKeyStatus(t *testing.T) {
	if got := ModelSigningKeyStatus("revoked"); got != model.SigningKeyStatusRevoked {
		t.Fatalf("revoked -> %v", got)
	}
	if got := ModelSigningKeyStatus("REVOKED"); got != model.SigningKeyStatusRevoked {
		t.Fatalf("REVOKED -> %v, want case-insensitive Revoked", got)
	}
	if got := ModelSigningKeyStatus("active"); got != model.SigningKeyStatusActive {
		t.Fatalf("active -> %v", got)
	}
	if got := ModelSigningKeyStatus(""); got != model.SigningKeyStatusActive {
		t.Fatalf("empty -> %v, want default Active", got)
	}
}

// Intent: ModelSigningKeyAlgorithm currently maps everything to ES256.
// FINDING: both branches of the function return SigningKeyAlgorithmEs256, so
// the strings.EqualFold check is dead code — a non-ES256 algorithm string is
// silently coerced to ES256 rather than surfaced. Behavior pinned as-is.
func TestModelSigningKeyAlgorithm(t *testing.T) {
	for _, in := range []string{"ES256", "es256", "RS256", "garbage", ""} {
		if got := ModelSigningKeyAlgorithm(in); got != model.SigningKeyAlgorithmEs256 {
			t.Fatalf("ModelSigningKeyAlgorithm(%q) = %v, want ES256 (current behavior)", in, got)
		}
	}
}

// Intent: ClaimReqsFromProto renders the proto map into the GraphQL pair list,
// preserving name/value; empty map -> nil.
func TestClaimReqsFromProto(t *testing.T) {
	if got := ClaimReqsFromProto(nil); got != nil {
		t.Fatalf("empty -> %v, want nil", got)
	}
	got := ClaimReqsFromProto(map[string]string{"plan": `"pro"`})
	if len(got) != 1 || got[0].Name != "plan" || got[0].JSONValue != `"pro"` {
		t.Fatalf("ClaimReqsFromProto = %+v", got)
	}
	if WebhookSecretMask() != "redacted" {
		t.Fatalf("WebhookSecretMask = %q, want redacted", WebhookSecretMask())
	}
}

// Intent: policyToModel maps a Commodore response into the GraphQL model,
// returning nil for a nil response or an unknown policy type.
func TestPolicyToModel(t *testing.T) {
	if policyToModel(nil) != nil {
		t.Fatal("nil response -> want nil model")
	}
	if policyToModel(&commodorepb.ResolvePlaybackPolicyResponse{Type: "weird"}) != nil {
		t.Fatal("unknown type -> want nil model")
	}
	got := policyToModel(&commodorepb.ResolvePlaybackPolicyResponse{Type: "public"})
	if got == nil || got.Type != model.PlaybackPolicyTypePublic {
		t.Fatalf("policyToModel(public) = %+v", got)
	}
}

// Intent: mapCommodoreErr maps gRPC status codes to the matching GraphQL union
// error member; nil error and non-status errors map to nil (opaque).
func TestMapCommodoreErr(t *testing.T) {
	if mapCommodoreErr(nil) != nil {
		t.Fatal("nil err -> want nil")
	}
	cases := []struct {
		code codes.Code
		want any
	}{
		{codes.NotFound, &model.NotFoundError{}},
		{codes.PermissionDenied, &model.AuthError{}},
		{codes.Unauthenticated, &model.AuthError{}},
		{codes.InvalidArgument, &model.ValidationError{}},
		{codes.ResourceExhausted, &model.RateLimitError{}},
	}
	for _, tc := range cases {
		got := mapCommodoreErr(status.Error(tc.code, "boom"))
		switch tc.want.(type) {
		case *model.NotFoundError:
			if _, ok := got.(*model.NotFoundError); !ok {
				t.Fatalf("%v -> %T, want *NotFoundError", tc.code, got)
			}
		case *model.AuthError:
			if _, ok := got.(*model.AuthError); !ok {
				t.Fatalf("%v -> %T, want *AuthError", tc.code, got)
			}
		case *model.ValidationError:
			if _, ok := got.(*model.ValidationError); !ok {
				t.Fatalf("%v -> %T, want *ValidationError", tc.code, got)
			}
		case *model.RateLimitError:
			if _, ok := got.(*model.RateLimitError); !ok {
				t.Fatalf("%v -> %T, want *RateLimitError", tc.code, got)
			}
		}
	}
	if got := mapCommodoreErr(status.Error(codes.Internal, "x")); got != nil {
		t.Fatalf("unmapped code -> %T, want nil", got)
	}
}
