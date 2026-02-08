package validation

import (
	"strings"
	"testing"
	"time"
)

func validBehavior() map[string]interface{} {
	return map[string]interface{}{
		"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
		"submittedAt": float64(time.Now().UnixMilli()),
		"mouse":       true,
		"typed":       true,
	}
}

func validRequest() *ContactRequest {
	return &ContactRequest{
		Name:       "Jane Doe",
		Email:      "jane@example.com",
		Message:    "Hello, this is a valid message.",
		HumanCheck: "human",
		Behavior:   validBehavior(),
	}
}

func TestValidateBot_HoneypotField(t *testing.T) {
	// Empty phone passes
	errs := ValidateBot(BotCheckParams{HumanCheck: "human", Behavior: validBehavior()})
	for _, e := range errs {
		if strings.Contains(e, "Honeypot") {
			t.Fatal("empty phone should not trigger honeypot")
		}
	}

	// Non-empty phone fails
	errs = ValidateBot(BotCheckParams{PhoneNumber: "555-1234", HumanCheck: "human", Behavior: validBehavior()})
	found := false
	for _, e := range errs {
		if strings.Contains(e, "Honeypot") {
			found = true
		}
	}
	if !found {
		t.Fatal("non-empty phone should trigger honeypot error")
	}
}

func TestValidateBot_HumanCheck(t *testing.T) {
	// "human" passes
	errs := ValidateBot(BotCheckParams{HumanCheck: "human", Behavior: validBehavior()})
	for _, e := range errs {
		if strings.Contains(e, "Human verification") {
			t.Fatal("'human' should pass human check")
		}
	}

	// "bot" fails
	errs = ValidateBot(BotCheckParams{HumanCheck: "bot", Behavior: validBehavior()})
	found := false
	for _, e := range errs {
		if strings.Contains(e, "Human verification") {
			found = true
		}
	}
	if !found {
		t.Fatal("'bot' should fail human check")
	}
}

func TestValidateBot_NilBehavior(t *testing.T) {
	errs := ValidateBot(BotCheckParams{HumanCheck: "human", Behavior: nil})
	found := false
	for _, e := range errs {
		if strings.Contains(e, "Missing behavioral data") {
			found = true
		}
	}
	if !found {
		t.Fatal("nil behavior should produce missing behavioral data error")
	}
}

func TestValidateBot_ValidBehavior(t *testing.T) {
	errs := ValidateBot(BotCheckParams{HumanCheck: "human", Behavior: validBehavior()})
	if len(errs) != 0 {
		t.Fatalf("expected no errors with valid params, got %v", errs)
	}
}

func TestValidateSubmission_NameBoundary(t *testing.T) {
	// 1 char name fails
	req := validRequest()
	req.Name = "A"
	errs := ValidateSubmission(req, true)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "Name is required") {
			found = true
		}
	}
	if !found {
		t.Fatal("1-char name should fail")
	}

	// 2 char name passes
	req.Name = "Ab"
	errs = ValidateSubmission(req, true)
	for _, e := range errs {
		if strings.Contains(e, "Name is required") {
			t.Fatal("2-char name should pass")
		}
	}
}

func TestValidateSubmission_MessageBoundary(t *testing.T) {
	// 9 chars fails
	req := validRequest()
	req.Message = "123456789"
	errs := ValidateSubmission(req, true)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "Message is required") {
			found = true
		}
	}
	if !found {
		t.Fatal("9-char message should fail")
	}

	// 10 chars passes
	req.Message = "1234567890"
	errs = ValidateSubmission(req, true)
	for _, e := range errs {
		if strings.Contains(e, "Message is required") {
			t.Fatal("10-char message should pass")
		}
	}
}

func TestValidateSubmission_SpamKeywords(t *testing.T) {
	// With spam keyword
	req := validRequest()
	req.Message = "I want to invest in crypto currency markets"
	errs := ValidateSubmission(req, true)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "spam keywords") {
			found = true
		}
	}
	if !found {
		t.Fatal("should detect spam keywords")
	}

	// Without spam keyword
	req.Message = "Hello, I want to discuss your streaming service."
	errs = ValidateSubmission(req, true)
	for _, e := range errs {
		if strings.Contains(e, "spam keywords") {
			t.Fatal("should not detect spam in clean message")
		}
	}
}

func TestValidateSubmission_Valid(t *testing.T) {
	errs := ValidateSubmission(validRequest(), true)
	if len(errs) != 0 {
		t.Fatalf("expected no errors for valid request, got %v", errs)
	}
}

func TestValidateBehavior_TimingBoundary(t *testing.T) {
	now := float64(time.Now().UnixMilli())

	// 2999ms → too fast
	err := validateBehavior(map[string]interface{}{
		"formShownAt": now,
		"submittedAt": now + 2999,
		"mouse":       true,
		"typed":       true,
	})
	if err == nil {
		t.Fatal("2999ms should be too fast")
	}
	if !strings.Contains(err.Error(), "too quickly") {
		t.Fatalf("expected 'too quickly' error, got: %v", err)
	}

	// 3000ms → passes
	err = validateBehavior(map[string]interface{}{
		"formShownAt": now,
		"submittedAt": now + 3000,
		"mouse":       true,
		"typed":       true,
	})
	if err != nil {
		t.Fatalf("3000ms should pass, got: %v", err)
	}
}

func TestValidateBehavior_ExpiryBoundary(t *testing.T) {
	now := float64(time.Now().UnixMilli())
	thirtyMin := float64(30 * time.Minute.Milliseconds())

	// Exactly 30 min → passes
	err := validateBehavior(map[string]interface{}{
		"formShownAt": now,
		"submittedAt": now + thirtyMin,
		"mouse":       true,
		"typed":       true,
	})
	if err != nil {
		t.Fatalf("exactly 30 min should pass, got: %v", err)
	}

	// 30 min + 1ms → expired
	err = validateBehavior(map[string]interface{}{
		"formShownAt": now,
		"submittedAt": now + thirtyMin + 1,
		"mouse":       true,
		"typed":       true,
	})
	if err == nil {
		t.Fatal("30min + 1ms should be expired")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected 'expired' error, got: %v", err)
	}
}

func TestValidateBehavior_NoInteraction(t *testing.T) {
	now := float64(time.Now().UnixMilli())

	err := validateBehavior(map[string]interface{}{
		"formShownAt": now,
		"submittedAt": now + 5000,
		"mouse":       false,
		"typed":       false,
	})
	if err == nil {
		t.Fatal("no interaction should fail")
	}
	if !strings.Contains(err.Error(), "no human interaction") {
		t.Fatalf("expected interaction error, got: %v", err)
	}
}

func TestValidateBehavior_InvalidFormat(t *testing.T) {
	err := validateBehavior(map[string]interface{}{
		"formShownAt": "not a number",
		"submittedAt": "also not",
	})
	if err == nil {
		t.Fatal("invalid format should fail")
	}
	if !strings.Contains(err.Error(), "invalid behavioral data") {
		t.Fatalf("expected format error, got: %v", err)
	}
}

func TestValidateBehavior_ArithmeticDirection(t *testing.T) {
	now := float64(time.Now().UnixMilli())

	// Ensure submittedAt - formShownAt is the correct direction
	// If gremlins swaps the subtraction, formShownAt - submittedAt would be negative → < 3000 → fail
	err := validateBehavior(map[string]interface{}{
		"formShownAt": now,
		"submittedAt": now + 5000,
		"mouse":       true,
		"typed":       true,
	})
	if err != nil {
		t.Fatalf("5s delay should pass, got: %v", err)
	}
}
