package middleware

import "testing"

// The allowlist is the JWT-bypass gate: isAllowlistedQuery/isAllowlistedOperation
// decide whether an unauthenticated GraphQL request may reach the executor
// (wired in public_or_jwt.go and public_graphql.go). A false positive here is an
// auth bypass, so these tests lock the contract field-by-field.
func TestIsAllowlistedOperation(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		opName    string
		want      bool
		rationale string
	}{
		{
			name:      "allowlisted query field passes",
			query:     `query { networkStatus { region } }`,
			want:      true,
			rationale: "networkStatus is in the read allowlist",
		},
		{
			name:      "non-allowlisted query field rejected",
			query:     `query { tenants { id } }`,
			want:      false,
			rationale: "tenants is not allowlisted; must require a JWT",
		},
		{
			name:      "mixed allowed and forbidden fields rejected",
			query:     `query { networkStatus { region } tenants { id } }`,
			want:      false,
			rationale: "any forbidden field in the selection fails the whole op",
		},
		{
			name:      "allowlisted mutation passes",
			query:     `mutation { walletLogin(input: {}) { token } }`,
			want:      true,
			rationale: "walletLogin carries its own per-call credential",
		},
		{
			name:      "bootstrapEdge mutation passes",
			query:     `mutation { bootstrapEdge(input: {}) { ok } }`,
			want:      true,
			rationale: "bootstrapEdge validates a one-time token server-side",
		},
		{
			name:      "query-allowlisted name used as a mutation field is rejected",
			query:     `mutation { networkStatus { region } }`,
			want:      false,
			rationale: "query and mutation allowlists are disjoint",
		},
		{
			name:      "mutation-allowlisted name used as a query field is rejected",
			query:     `query { walletLogin { token } }`,
			want:      false,
			rationale: "walletLogin is only public as a mutation",
		},
		{
			name:      "__typename alongside an allowed field passes",
			query:     `query { networkStatus { region } __typename }`,
			want:      true,
			rationale: "__typename is skipped, the real field gates the op",
		},
		{
			name:      "lone __typename passes (harmless introspection, not a data path)",
			query:     `query { __typename }`,
			want:      true,
			rationale: "current behavior: __typename-only selection is allowed",
		},
		{
			name:      "fragment spread into allowed fields passes",
			query:     `query { ...F } fragment F on Query { networkStatus { region } }`,
			want:      true,
			rationale: "fragments are traversed; their fields must be allowlisted",
		},
		{
			name:      "fragment spread into a forbidden field is rejected",
			query:     `query { ...F } fragment F on Query { tenants { id } }`,
			want:      false,
			rationale: "a forbidden field hidden in a fragment still fails",
		},
		{
			name:      "fragment cycle is rejected, not infinite-looped",
			query:     `query { ...A } fragment A on Query { ...B } fragment B on Query { ...A }`,
			want:      false,
			rationale: "the visited guard breaks the cycle and fails closed",
		},
		{
			name:      "undefined fragment is rejected",
			query:     `query { ...Missing }`,
			want:      false,
			rationale: "an unresolved fragment spread fails closed",
		},
		{
			name:      "inline fragment on allowed field passes",
			query:     `query { ... on Query { networkStatus { region } } }`,
			want:      true,
			rationale: "inline fragments recurse into their selection",
		},
		{
			name:      "inline fragment on forbidden field rejected",
			query:     `query { ... on Query { tenants { id } } }`,
			want:      false,
			rationale: "inline fragment fields are allowlisted too",
		},
		{
			name:      "subscription operation type is rejected",
			query:     `subscription { networkStatus { region } }`,
			want:      false,
			rationale: "only query and mutation operation types are allowlisted",
		},
		{
			name:      "operationName selects the matching op in a multi-op doc",
			query:     `query A { networkStatus { region } } query B { tenants { id } }`,
			opName:    "A",
			want:      true,
			rationale: "the named op A selects only allowed fields",
		},
		{
			name:      "operationName selecting a forbidden op is rejected",
			query:     `query A { networkStatus { region } } query B { tenants { id } }`,
			opName:    "B",
			want:      false,
			rationale: "the named op B selects a forbidden field",
		},
		{
			name:      "multi-op doc with no operationName is ambiguous and rejected",
			query:     `query A { networkStatus { region } } query B { networkStatus { region } }`,
			opName:    "",
			want:      false,
			rationale: "ambiguous operation cannot be resolved; fail closed",
		},
		{
			name:      "multi-op doc with unknown operationName is rejected",
			query:     `query A { networkStatus { region } } query B { networkStatus { region } }`,
			opName:    "C",
			want:      false,
			rationale: "no op matches the name and the doc is ambiguous",
		},
		{
			name:      "single-op doc ignores a mismatched operationName",
			query:     `query A { networkStatus { region } }`,
			opName:    "Z",
			want:      true,
			rationale: "with one op, the name is not used to disambiguate",
		},
		{
			name:      "alias on an allowed field still passes (uses real field name)",
			query:     `query { ns: networkStatus { region } }`,
			want:      true,
			rationale: "the allowlist matches node.Name, not the alias",
		},
		{
			name:      "forbidden field aliased to an allowed name is still rejected",
			query:     `query { networkStatus: tenants { id } }`,
			want:      false,
			rationale: "aliasing cannot smuggle a forbidden field past the gate",
		},
		{
			name:      "empty query string is rejected",
			query:     ``,
			want:      false,
			rationale: "no operation; fail closed",
		},
		{
			name:      "unparseable query is rejected",
			query:     `query {{{`,
			want:      false,
			rationale: "parse error fails closed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowlistedOperation(tc.query, tc.opName); got != tc.want {
				t.Fatalf("isAllowlistedOperation(%q, %q) = %v, want %v (%s)",
					tc.query, tc.opName, got, tc.want, tc.rationale)
			}
		})
	}
}

func TestIsAllowlistedQuery_JSONBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "valid body selecting an allowed field",
			body: `{"query":"query { networkStatus { region } }"}`,
			want: true,
		},
		{
			name: "valid body selecting a forbidden field",
			body: `{"query":"query { tenants { id } }"}`,
			want: false,
		},
		{
			name: "operationName honored from the JSON envelope",
			body: `{"query":"query A { networkStatus { region } } query B { tenants { id } }","operationName":"B"}`,
			want: false,
		},
		{
			name: "malformed JSON is rejected",
			body: `{not json`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowlistedQuery([]byte(tc.body)); got != tc.want {
				t.Fatalf("isAllowlistedQuery(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
