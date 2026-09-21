package notify

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func TestEmailContentUsesBrandLayoutAndEscapesFields(t *testing.T) {
	subject, body, err := emailContent(message{
		Headline: `[CRITICAL] Edge <b>down</b>`,
		Summary:  `<script>alert("x")</script>`,
		Fields:   []messageField{{Name: "Cluster", Value: `<img src=x onerror=alert(1)>`}},
		Link:     "https://app.example.test/app/admin/incidents/inc-1",
	}, config.EmailBranding{WebAppURL: "https://app.example.test/app", SupportEmail: "help@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://app.example.test/app/frameworks-light-logomark.png", "help@example.test"} {
		if !strings.Contains(body, want) {
			t.Fatalf("email missing configured branding %q", want)
		}
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
