package validation

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type ContactRequest struct {
	Name          string                 `json:"name"`
	Email         string                 `json:"email"`
	Company       string                 `json:"company"`
	Message       string                 `json:"message"`
	PhoneNumber   string                 `json:"phone_number"`
	HumanCheck    string                 `json:"human_check"`
	Behavior      map[string]interface{} `json:"behavior"`
	TurnstileToken string                `json:"turnstileToken"`
}

var emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

var spamKeywords = []string{
	"crypto", "bitcoin", "investment", "loan", "casino", "viagra", "pharmacy",
}

func ValidateSubmission(req *ContactRequest, turnstileEnabled bool) []string {
	var errors []string

	legacyBotChecksEnabled := !turnstileEnabled

	if legacyBotChecksEnabled {
		if strings.TrimSpace(req.PhoneNumber) != "" {
			errors = append(errors, "Honeypot field filled (bot detected)")
		}

		if req.HumanCheck != "human" {
			errors = append(errors, "Human verification not selected")
		}

		if req.Behavior != nil {
			if err := validateBehavior(req.Behavior); err != nil {
				errors = append(errors, err.Error())
			}
		} else {
			errors = append(errors, "Missing behavioral data")
		}
	}

	if len(strings.TrimSpace(req.Name)) < 2 {
		errors = append(errors, "Name is required (minimum 2 characters)")
	}

	if !emailRegex.MatchString(req.Email) {
		errors = append(errors, "Valid email is required")
	}

	if len(strings.TrimSpace(req.Message)) < 10 {
		errors = append(errors, "Message is required (minimum 10 characters)")
	}

	content := strings.ToLower(fmt.Sprintf("%s %s %s %s",
		req.Name, req.Email, req.Company, req.Message))

	var foundSpam []string
	for _, keyword := range spamKeywords {
		if strings.Contains(content, keyword) {
			foundSpam = append(foundSpam, keyword)
		}
	}

	if len(foundSpam) > 0 {
		errors = append(errors, fmt.Sprintf("Potential spam keywords detected: %s",
			strings.Join(foundSpam, ", ")))
	}

	return errors
}

func validateBehavior(behavior map[string]interface{}) error {
	formShownAt, ok1 := behavior["formShownAt"].(float64)
	submittedAt, ok2 := behavior["submittedAt"].(float64)

	if !ok1 || !ok2 {
		return fmt.Errorf("invalid behavioral data format")
	}

	timeSpent := int64(submittedAt - formShownAt)

	if timeSpent < 3000 {
		return fmt.Errorf("form submitted too quickly")
	}

	mouse, _ := behavior["mouse"].(bool)
	typed, _ := behavior["typed"].(bool)

	if !mouse && !typed {
		return fmt.Errorf("no human interaction detected")
	}

	if timeSpent > int64(30*time.Minute.Milliseconds()) {
		return fmt.Errorf("form session expired")
	}

	return nil
}
