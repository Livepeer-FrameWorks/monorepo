package resolvers

import "testing"

func TestSanitizeSkipperJSONStripsSensitiveKeys(t *testing.T) {
	input := map[string]any{
		"token":     "secret",
		"safe":      "ok",
		"_internal": "drop",
		"nested": map[string]any{
			"password": "pw",
			"value":    1,
		},
	}

	out, ok := sanitizeSkipperJSON(input).(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", sanitizeSkipperJSON(input))
	}

	if _, exists := out["token"]; exists {
		t.Fatalf("expected token to be removed")
	}
	if _, exists := out["_internal"]; exists {
		t.Fatalf("expected internal key to be removed")
	}
	if out["safe"] != "ok" {
		t.Fatalf("expected safe key to be preserved")
	}

	nested, ok := out["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map, got %T", out["nested"])
	}
	if _, exists := nested["password"]; exists {
		t.Fatalf("expected nested password to be removed")
	}
	if nested["value"] != 1 {
		t.Fatalf("expected nested value to be preserved")
	}
}

func TestSanitizeSkipperJSONTruncatesStrings(t *testing.T) {
	long := make([]rune, skipperMaxStringLen+10)
	for i := range long {
		long[i] = 'a'
	}

	out, ok := sanitizeSkipperJSON(string(long)).(string)
	if !ok {
		t.Fatalf("expected string output, got %T", sanitizeSkipperJSON(string(long)))
	}
	if len(out) != skipperMaxStringLen {
		t.Fatalf("expected truncated length %d, got %d", skipperMaxStringLen, len(out))
	}
}

func TestSanitizeSkipperJSONDepthLimit(t *testing.T) {
	input := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": map[string]any{
					"d": map[string]any{
						"e": map[string]any{
							"f": map[string]any{
								"g": "deep",
							},
						},
					},
				},
			},
		},
	}

	if sanitizeSkipperJSON(input) != nil {
		t.Fatalf("expected deep payload to be dropped")
	}
}
