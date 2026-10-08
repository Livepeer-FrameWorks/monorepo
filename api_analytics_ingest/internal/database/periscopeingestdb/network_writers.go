package periscopeingestdb

import (
	"context"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type NetworkObservationRow struct {
	TenantID        uuid.UUID
	EventID         uuid.UUID
	Timestamp       time.Time
	Metric          obs.Metric
	PublicData      string
	PrivateTenantID uuid.UUID
	PrivateData     string
}

func PrepareNetworkObservation(ctx context.Context, db BatchPreparer) (*Writer[NetworkObservationRow], error) {
	return prepare(ctx, db, `INSERT INTO network_observations (
		tenant_id, provider, network_id, environment, timestamp, event_id,
		gateway_id, gateway_region, producer_kind, kind, resource_id, endpoint_uri, resolved_ip,
		workload, profile_key, success, qualifying, failure_kind, disposition,
		latency_ms, source_duration_ms, response_headers_ms, response_body_ms,
		attempts, fallback_count, deadline_exceeded, cadence_seconds,
		price_numerator, price_denominator, currency, price_unit,
		public_data, private_tenant_id, private_data,
		accounting_version, request_dispatched, returned_pixels_known, returned_pixels, accepted_pixels, accepted_renditions,
		service_charge_known, service_charge_numerator, service_charge_denominator,
		ticket_credit_known, ticket_credit_numerator, ticket_credit_denominator, tickets_sent
	)`, func(row NetworkObservationRow) []interface{} {
		m := row.Metric
		numerator, denominator, currency, unit := "", "", "", ""
		if m.Price != nil {
			numerator, denominator, currency, unit = m.Price.Numerator, m.Price.Denominator, m.Price.Currency, m.Price.Unit
		}
		flag := func(value bool) uint8 {
			if value {
				return 1
			}
			return 0
		}
		work := obs.WorkFact{}
		if m.Work != nil {
			work = *m.Work
		}
		amount := func(a *obs.Amount) (uint8, decimal.Decimal, string) {
			if a == nil || a.Validate() != nil || len(a.Numerator) > 50 {
				return 0, decimal.Zero, "1"
			}
			n, err := decimal.NewFromString(a.Numerator)
			if err != nil {
				return 0, decimal.Zero, "1"
			}
			return 1, n, a.Denominator
		}
		chargeKnown, chargeNumerator, chargeDenominator := amount(work.ServiceCharge)
		creditKnown, creditNumerator, creditDenominator := uint8(0), "0", "1"
		if a := work.TicketCreditSent; a != nil && a.Validate() == nil {
			creditKnown, creditNumerator, creditDenominator = 1, a.Numerator, a.Denominator
		}
		return []interface{}{
			row.TenantID, m.Provider, m.Network, m.Environment, row.Timestamp, row.EventID,
			m.ObserverID, m.Region, m.ProducerKind, m.Kind, m.ResourceID, m.URI, m.IP,
			m.Workload, m.ProfileKey, flag(m.Success), flag(m.Qualifying), m.FailureKind, m.Disposition,
			m.LatencyMS, m.SourceDurationMS, m.ResponseHeadersMS, m.ResponseBodyMS,
			m.Attempts, m.FallbackCount, flag(m.DeadlineExceeded), m.CadenceSeconds,
			numerator, denominator, currency, unit, row.PublicData, row.PrivateTenantID, row.PrivateData,
			work.Version, flag(work.RequestDispatched), flag(work.ReturnedPixelsKnown), work.ReturnedPixels, work.AcceptedPixels, work.AcceptedRenditions,
			chargeKnown, chargeNumerator, chargeDenominator, creditKnown, creditNumerator, creditDenominator, work.TicketsSent,
		}
	})
}
