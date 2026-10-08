package grpc

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

func (s *PeriscopeServer) networkWork(ctx context.Context, where string, args []any, start, end time.Time, rolled bool) (*obs.WorkHistory, error) {
	// Endpoint-wide decision rows have no work and are not dimensions of work snapshots.
	where = strings.ReplaceAll(where, "(resolved_ip=? OR (kind='decision' AND resolved_ip=''))", "resolved_ip=?")
	work := &obs.WorkHistory{Scope: "frameworks-gateways", Start: start, End: end, Summaries: []obs.WorkSummary{}}
	table := "periscope.network_observations"
	columns := `countIf(kind='attempt' AND accounting_version=1),countIf(accounting_version!=1),
 countIf(kind='attempt' AND accounting_version=1 AND request_dispatched=1),
 sumIf(toUInt64(source_duration_ms),kind='attempt' AND accounting_version=1 AND request_dispatched=1),
 countIf(kind='attempt' AND accounting_version=1 AND returned_pixels_known=1),
 sumIf(returned_pixels,kind='attempt' AND accounting_version=1 AND returned_pixels_known=1),
 countIf(kind='segment' AND accounting_version=1 AND success=1),
 sumIf(toUInt64(source_duration_ms),kind='segment' AND accounting_version=1 AND success=1),
 sumIf(accepted_pixels,kind='segment' AND accounting_version=1 AND success=1),
 sumIf(toUInt64(source_duration_ms)*toUInt64(accepted_renditions),kind='segment' AND accounting_version=1 AND success=1),
 countIf(service_charge_known=1),toString(sumIf(service_charge_numerator,service_charge_known=1)),
 countIf(ticket_credit_known=1),sum(tickets_sent),
 countIf(kind='attempt' AND accounting_version=1 AND request_dispatched=1 AND source_duration_ms>0),
 countIf(kind='segment' AND accounting_version=1 AND success=1 AND source_duration_ms>0),
 countIf(kind='segment' AND accounting_version=1 AND success=1 AND accepted_renditions>0)`
	queryArgs := append([]any(nil), args...)
	if rolled {
		table = "periscope.network_work_rollups"
		work.IntervalSeconds = 3600
		// Retained totals use only whole hours contained in the requested range.
		work.Start = start.Truncate(time.Hour)
		if work.Start.Before(start) {
			work.Start = work.Start.Add(time.Hour)
		}
		work.End = end.Truncate(time.Hour)
		queryArgs[4], queryArgs[5] = work.Start, work.End
		where = strings.ReplaceAll(where, "timestamp", "bucket") + " AND interval_seconds=3600"
		columns = `sum(accounted_attempts),sum(unaccounted_samples),sum(dispatched_attempts),sum(dispatched_source_ms),
 sum(returned_attempts),sum(returned_pixels),sum(accepted_segments),sum(accepted_source_ms),sum(accepted_pixels),sum(accepted_rendition_ms),
 sum(service_charge_samples),toString(sum(service_charge_numerator)),sum(ticket_credit_samples),sum(tickets_sent),
 sum(dispatched_duration_samples),sum(accepted_duration_samples),sum(accepted_output_samples)`
	} else {
		where += " AND kind IN ('attempt','segment')"
	}
	// Denominators remain separate until exact rational addition in Go; no rate or amount is rounded.
	query := fmt.Sprintf(`SELECT resource_id,resolved_ip,gateway_region,profile_key,service_charge_denominator,%s
 FROM %s FINAL WHERE %s
 GROUP BY resource_id,resolved_ip,gateway_region,profile_key,service_charge_denominator
 ORDER BY resource_id,resolved_ip,gateway_region,profile_key LIMIT 10001`, columns, table, where)
	rows, err := periscopequerydb.Query(ctx, s.clickhouse, query, queryArgs...)
	if err != nil {
		return nil, wrapClickhouseError(err, "network work unavailable")
	}
	defer rows.Close()
	type key struct{ resource, ip, region, profile string }
	summaries := map[key]*obs.WorkSummary{}
	charges, credits := map[key]*big.Rat{}, map[key]*big.Rat{}
	count := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var c obs.WorkSummary
		var chargeDenominator, chargeNumerator string
		if err := rows.Scan(&c.ResourceID, &c.IP, &c.Region, &c.ProfileKey, &chargeDenominator,
			&c.AccountedAttempts, &c.UnaccountedSamples, &c.DispatchedAttempts, &c.DispatchedSourceMS, &c.ReturnedAttempts, &c.ReturnedPixels,
			&c.AcceptedSegments, &c.AcceptedSourceMS, &c.AcceptedPixels, &c.AcceptedRenditionMS,
			&c.ServiceChargeSamples, &chargeNumerator, &c.TicketCreditSamples, &c.TicketsSent,
			&c.DispatchedDurationSamples, &c.AcceptedDurationSamples, &c.AcceptedOutputSamples); err != nil {
			return nil, wrapClickhouseError(err, "network work scan failed")
		}
		count++
		if count > 10000 {
			work.Truncated = true
			work.Summaries = []obs.WorkSummary{}
			return work, nil
		}
		k := key{c.ResourceID, c.IP, c.Region, c.ProfileKey}
		if summaries[k] == nil {
			summaries[k] = &obs.WorkSummary{ResourceID: c.ResourceID, IP: c.IP, Region: c.Region, ProfileKey: c.ProfileKey}
			charges[k] = new(big.Rat)
			credits[k] = new(big.Rat)
		}
		total := summaries[k]
		total.AccountedAttempts += c.AccountedAttempts
		total.UnaccountedSamples += c.UnaccountedSamples
		total.DispatchedAttempts += c.DispatchedAttempts
		total.DispatchedSourceMS += c.DispatchedSourceMS
		total.ReturnedAttempts += c.ReturnedAttempts
		total.ReturnedPixels += c.ReturnedPixels
		total.AcceptedSegments += c.AcceptedSegments
		total.AcceptedSourceMS += c.AcceptedSourceMS
		total.AcceptedPixels += c.AcceptedPixels
		total.AcceptedRenditionMS += c.AcceptedRenditionMS
		total.ServiceChargeSamples += c.ServiceChargeSamples
		total.TicketCreditSamples += c.TicketCreditSamples
		total.TicketsSent += c.TicketsSent
		total.DispatchedDurationSamples += c.DispatchedDurationSamples
		total.AcceptedDurationSamples += c.AcceptedDurationSamples
		total.AcceptedOutputSamples += c.AcceptedOutputSamples
		value, ok := new(big.Rat).SetString(chargeNumerator + "/" + chargeDenominator)
		if !ok || value.Sign() < 0 {
			return nil, fmt.Errorf("invalid retained accounting amount")
		}
		charges[k].Add(charges[k], value)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "network work query failed")
	}
	if err := rows.Close(); err != nil {
		return nil, wrapClickhouseError(err, "network work close failed")
	}
	// Ticket values vary independently of work cohorts. Bound their exact projection separately.
	if len(summaries) > 0 {
		creditSamples, creditWhere := "count()", "ticket_credit_known=1"
		if rolled {
			creditSamples, creditWhere = "sum(ticket_credit_samples)", "ticket_credit_samples>0"
		}
		creditQuery := fmt.Sprintf(`SELECT resource_id,resolved_ip,gateway_region,profile_key,ticket_credit_denominator,ticket_credit_numerator,%s
 FROM %s FINAL WHERE %s AND %s AND ticket_credit_numerator!='0'
 GROUP BY resource_id,resolved_ip,gateway_region,profile_key,ticket_credit_denominator,ticket_credit_numerator
 ORDER BY resource_id,resolved_ip,gateway_region,profile_key LIMIT 10001`, creditSamples, table, where, creditWhere)
		creditRows, err := periscopequerydb.Query(ctx, s.clickhouse, creditQuery, queryArgs...)
		if err != nil {
			return nil, wrapClickhouseError(err, "network ticket credit unavailable")
		}
		defer creditRows.Close()
		count = 0
		for creditRows.Next() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var k key
			var numerator, denominator string
			var samples uint64
			if err := creditRows.Scan(&k.resource, &k.ip, &k.region, &k.profile, &denominator, &numerator, &samples); err != nil {
				return nil, wrapClickhouseError(err, "network ticket credit scan failed")
			}
			count++
			if count > 10000 {
				work.TicketCreditTruncated = true
				break
			}
			value, ok := new(big.Rat).SetString(numerator + "/" + denominator)
			if !ok || value.Sign() < 0 {
				return nil, fmt.Errorf("invalid retained ticket credit")
			}
			if credits[k] != nil {
				value.Mul(value, new(big.Rat).SetInt(new(big.Int).SetUint64(samples)))
				credits[k].Add(credits[k], value)
			}
		}
		if err := creditRows.Err(); err != nil {
			return nil, wrapClickhouseError(err, "network ticket credit query failed")
		}
	}
	for k, total := range summaries {
		if total.ServiceChargeSamples > 0 {
			total.ServiceCharge = obs.AmountFromRat(charges[k])
		}
		if total.TicketCreditSamples > 0 {
			total.TicketCreditIncomplete = work.TicketCreditTruncated
			if !total.TicketCreditIncomplete {
				total.TicketCreditSent = obs.AmountFromRat(credits[k])
			}
		}
		work.Summaries = append(work.Summaries, *total)
	}
	sort.Slice(work.Summaries, func(i, j int) bool {
		a, b := work.Summaries[i], work.Summaries[j]
		return a.ResourceID+"|"+a.IP+"|"+a.Region+"|"+a.ProfileKey < b.ResourceID+"|"+b.IP+"|"+b.Region+"|"+b.ProfileKey
	})
	return work, nil
}
