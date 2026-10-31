package billing

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var operatorGrantAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

// OperatorGrantSpec is the shared desired-state contract for operator billing grants.
type OperatorGrantSpec struct {
	BaseFee    *string `yaml:"base_fee,omitempty"`
	WaiveUsage bool    `yaml:"waive_usage,omitempty"`
	Collection string  `yaml:"collection,omitempty"`
	Until      string  `yaml:"until,omitempty"`
	Reason     string  `yaml:"reason"`
}

// Validate checks the arrangement fields and parses its optional deadline.
func (g OperatorGrantSpec) Validate() error {
	if strings.TrimSpace(g.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	if g.Collection != "" && g.Collection != "provider" && g.Collection != "invoice" {
		return fmt.Errorf("collection must be provider or invoice")
	}
	if g.BaseFee != nil {
		if _, err := ParseOperatorGrantAmount(*g.BaseFee); err != nil {
			return fmt.Errorf("base_fee: %w", err)
		}
	}
	if g.BaseFee == nil && !g.WaiveUsage && g.Collection != "invoice" {
		return fmt.Errorf("grant changes nothing")
	}
	_, err := g.Expiry()
	return err
}

// Expiry returns the absolute UTC deadline, or zero when the grant has none.
func (g OperatorGrantSpec) Expiry() (time.Time, error) {
	g.Until = strings.TrimSpace(g.Until)
	if g.Until == "" {
		return time.Time{}, nil
	}
	if day, err := time.Parse(time.DateOnly, g.Until); err == nil {
		return day.AddDate(0, 0, 1), nil
	}
	at, err := time.Parse(time.RFC3339, g.Until)
	if err != nil {
		return time.Time{}, fmt.Errorf("until must be a date or RFC3339 timestamp")
	}
	return at.UTC(), nil
}

// ParseOperatorGrantAmount validates a non-negative EUR amount without float conversion.
func ParseOperatorGrantAmount(value string) (string, error) {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, ".", 2)
	if !operatorGrantAmountPattern.MatchString(value) || len(strings.TrimLeft(parts[0], "0")) > 8 {
		return "", fmt.Errorf("expected a non-negative EUR amount with at most eight integer digits and two decimals")
	}
	return value, nil
}
