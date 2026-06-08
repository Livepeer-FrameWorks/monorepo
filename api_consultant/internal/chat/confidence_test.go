package chat

import "testing"

func TestConfidenceFromSourceType(t *testing.T) {
	cases := []struct {
		source SourceType
		want   Confidence
	}{
		{SourceTypeKnowledgeBase, ConfidenceVerified},
		{SourceTypeWeb, ConfidenceSourced},
		{SourceTypeLLM, ConfidenceBestGuess},
		{SourceTypeUnknown, ConfidenceUnknown},
		{SourceType("something-else"), ConfidenceUnknown},
	}

	for _, tc := range cases {
		t.Run(string(tc.source), func(t *testing.T) {
			if got := ConfidenceFromSourceType(tc.source); got != tc.want {
				t.Fatalf("ConfidenceFromSourceType(%q) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}
