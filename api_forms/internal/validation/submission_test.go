package validation

import (
	"testing"
	"time"
)

func TestValidateSubmissionRequiresFields(t *testing.T) {
	req := &ContactRequest{
		Name:    "A",
		Email:   "bad-email",
		Message: "short",
	}

	errors := ValidateSubmission(req, false)
	if len(errors) == 0 {
		t.Fatalf("expected validation errors")
	}
}

func TestValidateSubmissionDetectsSpamKeywords(t *testing.T) {
	req := &ContactRequest{
		Name:       "Jane Doe",
		Email:      "jane@example.com",
		Message:    "This includes crypto investment options.",
		HumanCheck: "human",
		Behavior: map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}

	errors := ValidateSubmission(req, false)
	if len(errors) == 0 {
		t.Fatalf("expected spam keyword error")
	}
}

func TestValidateBotMissingBehavior(t *testing.T) {
	errors := ValidateBot(BotCheckParams{
		PhoneNumber: "",
		HumanCheck:  "human",
		Behavior:    nil,
	})
	if len(errors) == 0 {
		t.Fatalf("expected bot validation errors")
	}
}

func TestValidateBehaviorTiming(t *testing.T) {
	behavior := map[string]interface{}{
		"formShownAt": float64(time.Now().UnixMilli()),
		"submittedAt": float64(time.Now().Add(1 * time.Second).UnixMilli()),
		"mouse":       true,
		"typed":       true,
	}

	if err := validateBehavior(behavior); err == nil {
		t.Fatalf("expected timing error")
	}
}
