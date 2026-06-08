package grpc

import (
	"strings"
	"testing"
)

// policyTarget drives which table and which WHERE shape a playback-policy
// mutation uses. The invariant that matters: VOD and clip targets are
// addressable by EITHER their UUID or their content hash, while streams are
// UUID-only. A regression here would let a hash-addressed policy update silently
// match nothing (or the wrong column placeholder).
func TestPolicyTargetSQLFragments(t *testing.T) {
	tests := []struct {
		kind           string
		wantTable      string
		wantUpdateHash bool // WHERE allows hash OR id at $4
		wantLookupHash bool // WHERE allows hash OR id at $1
	}{
		{kind: "stream", wantTable: "streams", wantUpdateHash: false, wantLookupHash: false},
		{kind: "vod_asset", wantTable: "vod_assets", wantUpdateHash: true, wantLookupHash: true},
		{kind: "clip", wantTable: "clips", wantUpdateHash: true, wantLookupHash: true},
		{kind: "unknown", wantTable: "", wantUpdateHash: false, wantLookupHash: false},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			target := policyTarget{kind: tc.kind, id: "x"}
			if got := target.tableColumn(); got != tc.wantTable {
				t.Errorf("tableColumn() = %q, want %q", got, tc.wantTable)
			}

			upd := targetPolicyUpdateWhere(target)
			if tc.wantUpdateHash {
				if !strings.Contains(upd, "_hash = $4") || !strings.Contains(upd, "id::text = $4") {
					t.Errorf("update WHERE %q must allow hash OR id at $4", upd)
				}
			} else if upd != "id::text = $4" {
				t.Errorf("update WHERE = %q, want id-only $4", upd)
			}

			look := targetPolicyLookupWhere(target)
			if tc.wantLookupHash {
				if !strings.Contains(look, "_hash = $1") || !strings.Contains(look, "id::text = $1") {
					t.Errorf("lookup WHERE %q must allow hash OR id at $1", look)
				}
			} else if look != "id::text = $1" {
				t.Errorf("lookup WHERE = %q, want id-only $1", look)
			}
		})
	}
}
