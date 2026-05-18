package control

import (
	"context"
	"testing"
)

// TestDVRChapterPolicyHelpers_NoCommodore pins three contracts:
//
//  1. Accept both the canonical "dvr+<token>" form and the bare token.
//     mist.ExtractInternalName now strips dvr+ in the USER_NEW path so
//     the bare token is the common shape, but resolve-time call sites
//     can still pass the full stream name.
//  2. Return the input unchanged when no Commodore client is wired —
//     pure rewrite helpers, no deny.
//  3. Return the input unchanged for non-chapter tokens. Downstream
//     policy lookup keys on the input directly (DVR artifact
//     internal_name → DVR policy, anything else → not-found path).
func TestDVRChapterPolicyHelpers_NoCommodore(t *testing.T) {
	prev := CommodoreClient
	CommodoreClient = nil
	defer func() { CommodoreClient = prev }()

	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"bare chapter id (prefix already stripped by USER_NEW)", "chap-1"},
		{"prefixed chapter id", "dvr+chap-1"},
		{"bare dvr artifact internal_name", "dvr_int_001"},
		{"prefixed dvr artifact internal_name", "dvr+dvr_int_001"},
		{"bare live internal name", "stream_abc"},
		{"empty token after prefix", "dvr+"},
	}
	for _, tc := range cases {
		t.Run("internal_name/"+tc.name, func(t *testing.T) {
			if got := DVRChapterPolicyInternalName(context.Background(), tc.input); got != tc.input {
				t.Errorf("nil-Commodore must pass through; got %q want %q", got, tc.input)
			}
		})
		t.Run("playback_id/"+tc.name, func(t *testing.T) {
			if got := DVRChapterPolicyPlaybackID(context.Background(), tc.input); got != tc.input {
				t.Errorf("nil-Commodore must pass through; got %q want %q", got, tc.input)
			}
		})
	}
}
