package social

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSocialEmailUsesBrandLayoutAndEscapesContent(t *testing.T) {
	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.test/app")
	body, err := renderSocialEmail(PostRecord{
		TweetText:      `<script>alert("x")</script> Ship it`,
		ContentType:    ContentPlatformStats,
		ContextSummary: "A useful update",
		TriggerData:    map[string]any{"active_streams": float64(12)},
		CreatedAt:      time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FrameWorks", "#0f4b6e", "Social post draft", "Active Streams: 12"} {
		if !strings.Contains(body, want) {
			t.Fatalf("email missing %q", want)
		}
	}
	if strings.Contains(body, `<script>`) || !strings.Contains(body, `&lt;script&gt;`) {
		t.Fatalf("tweet content was not escaped: %s", body)
	}
}
