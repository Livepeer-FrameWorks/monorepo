package networkobservation

import (
	"fmt"
	"math/big"
	"time"
)

// Amount is an exact rational quantity; nil denotes missing accounting evidence.
type Amount struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Currency    string `json:"currency"`
	Unit        string `json:"unit"`
}

// Ticket credit includes a 256-bit probability fraction and may exceed decimal precision.
func (a Amount) Validate() error {
	if a.Currency != "ETH" || a.Unit != "wei" {
		return fmt.Errorf("unsupported accounting denomination")
	}
	for index, value := range []string{a.Numerator, a.Denominator} {
		limit := 200
		if index == 1 {
			limit = 100
		}
		if len(value) == 0 || len(value) > limit {
			return fmt.Errorf("accounting amount outside supported precision")
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return fmt.Errorf("invalid accounting integer")
			}
		}
	}
	denominator, _ := new(big.Int).SetString(a.Denominator, 10)
	if denominator.Sign() == 0 {
		return fmt.Errorf("zero accounting denominator")
	}
	return nil
}

func (a Amount) Rat() (*big.Rat, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	n, _ := new(big.Int).SetString(a.Numerator, 10)
	d, _ := new(big.Int).SetString(a.Denominator, 10)
	return new(big.Rat).SetFrac(n, d), nil
}

func AmountFromRat(r *big.Rat) *Amount {
	if r == nil {
		return nil
	}
	return &Amount{Numerator: r.Num().String(), Denominator: r.Denom().String(), Currency: "ETH", Unit: "wei"}
}

type WorkFact struct {
	Version             uint32  `json:"version"`
	RequestDispatched   bool    `json:"request_dispatched"`
	ReturnedPixelsKnown bool    `json:"returned_pixels_known"`
	ReturnedPixels      uint64  `json:"returned_pixels,string"`
	AcceptedPixels      uint64  `json:"accepted_pixels,string"`
	AcceptedRenditions  uint32  `json:"accepted_renditions"`
	ServiceCharge       *Amount `json:"service_charge,omitempty"`
	TicketCreditSent    *Amount `json:"ticket_credit_sent,omitempty"`
	TicketsSent         uint64  `json:"tickets_sent,string"`
}

// WorkSummary counts final accepted segments separately from every dispatched attempt.
type WorkSummary struct {
	ResourceID                string  `json:"resource_id"`
	IP                        string  `json:"ip"`
	Region                    string  `json:"region"`
	ProfileKey                string  `json:"profile_key"`
	AccountedAttempts         uint64  `json:"accounted_attempts"`
	UnaccountedSamples        uint64  `json:"unaccounted_samples"`
	DispatchedAttempts        uint64  `json:"dispatched_attempts"`
	DispatchedSourceMS        uint64  `json:"dispatched_source_ms,string"`
	ReturnedAttempts          uint64  `json:"returned_attempts"`
	ReturnedPixels            uint64  `json:"returned_pixels,string"`
	AcceptedSegments          uint64  `json:"accepted_segments"`
	AcceptedSourceMS          uint64  `json:"accepted_source_ms,string"`
	AcceptedPixels            uint64  `json:"accepted_pixels,string"`
	AcceptedRenditionMS       uint64  `json:"accepted_rendition_ms,string"`
	ServiceChargeSamples      uint64  `json:"service_charge_samples"`
	ServiceCharge             *Amount `json:"service_charge,omitempty"`
	TicketCreditSamples       uint64  `json:"ticket_credit_samples"`
	TicketCreditSent          *Amount `json:"ticket_credit_sent,omitempty"`
	TicketsSent               uint64  `json:"tickets_sent,string"`
	DispatchedDurationSamples uint64  `json:"dispatched_duration_samples"`
	AcceptedDurationSamples   uint64  `json:"accepted_duration_samples"`
	AcceptedOutputSamples     uint64  `json:"accepted_output_samples"`
}

type WorkHistory struct {
	Scope           string        `json:"scope"`
	Start           time.Time     `json:"start"`
	End             time.Time     `json:"end"`
	IntervalSeconds int           `json:"interval_seconds"`
	Summaries       []WorkSummary `json:"summaries"`
	Truncated       bool          `json:"truncated"`
}
