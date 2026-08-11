package artifacts

import (
	"errors"
	"testing"
)

// The multipart ownership fence must fail closed on a missing or foreign recorded backend, and pass ONLY on an exact
// match — presence alone is never enough.
func TestVerifyLocalMultipartOwnership(t *testing.T) {
	const local = "cell-eu-backend"
	cases := []struct {
		name            string
		recorded, local string
		wantErr         error
	}{
		{"exact match owned", local, local, nil},
		{"missing recorded", "", local, ErrBackendUnattributed},
		{"no local store", local, "", ErrBackendUnattributed},
		{"both empty", "", "", ErrBackendUnattributed},
		{"foreign backend", "cell-us-backend", local, ErrBackendForeign},
		{"presence but different", "some-nonempty-id", local, ErrBackendForeign},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyLocalMultipartOwnership(tc.recorded, tc.local)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifyLocalMultipartOwnership(%q, %q) = %v, want %v", tc.recorded, tc.local, err, tc.wantErr)
			}
		})
	}
}
