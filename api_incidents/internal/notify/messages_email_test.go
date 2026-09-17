package notify

import (
	"strings"
	"testing"
)

func TestEmailContentUsesBrandLayoutAndEscapesFields(t *testing.T) {
	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.test/app")
	subject, body, err := emailContent(message{
		Headline: `[CRITICAL] Edge <b>down</b>`,
		Summary:  `<script>alert("x")</script>`,
		Fields:   []messageField{{Name: "Cluster", Value: `<img src=x onerror=alert(1)>`}},
		Link:     "https://app.example.test/app/admin/incidents/inc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "[Lookout] [CRITICAL] Edge <b>down</b>" {
		t.Fatalf("subject = %q", subject)
	}
	for _, want := range []string{"FrameWorks", "#0f4b6e", "Open incident", "&lt;script&gt;", "&lt;img"} {
		if !strings.Contains(body, want) {
			t.Fatalf("email missing %q", want)
		}
	}
	if strings.Contains(body, `<script>`) || strings.Contains(body, `<img src=x`) {
		t.Fatalf("incident content was not escaped: %s", body)
	}
}
