package pricing

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/rating"
	"github.com/shopspring/decimal"
)

type PlacementUsageStatus string

const (
	PlacementUsageNotRequired    PlacementUsageStatus = "not_required"
	PlacementUsageUnknownPeriod  PlacementUsageStatus = "unknown_period"
	PlacementUsageNoClosedWindow PlacementUsageStatus = "no_closed_window"
	PlacementUsageMissingSources PlacementUsageStatus = "missing_sources"
	PlacementUsageMissingWindows PlacementUsageStatus = "missing_windows"
	PlacementUsageOpenAnomalies  PlacementUsageStatus = "open_anomalies"
	PlacementUsageUnattributed   PlacementUsageStatus = "unattributed_usage"
	PlacementUsageInvalid        PlacementUsageStatus = "invalid_usage"
	PlacementUsageCovered        PlacementUsageStatus = "covered_through_cutoff"
)

// PlacementAllowanceUsage describes exact recorded consumption through Through,
// not instantaneous usage after that cutoff. Totals exist only with reporting
// coverage and valid attribution/units; callers must preserve this distinction
// when presenting or using allowance-sensitive comparison estimates.
type PlacementAllowanceUsage struct {
	Status                                       PlacementUsageStatus
	PeriodStart, PeriodEnd, Through              time.Time
	ActiveSources, MissingWindows, OpenAnomalies int64
	Totals                                       map[string]map[rating.Meter]decimal.Decimal
}

func placementNeedsAllowanceUsage(snapshot *PlacementTariffSnapshot) bool {
	for _, cluster := range snapshot.Clusters {
		for _, rule := range cluster.MeteredRules {
			if (rule.Meter == rating.MeterIngressGB || rule.Meter == rating.MeterEgressGB || rule.Meter == rating.MeterDeliveredMinutes) && rule.Model == rating.ModelTieredGraduated && rule.IncludedQuantity.IsPositive() && rule.UnitPrice.IsPositive() {
				return true
			}
		}
	}
	return false
}

func readPlacementAllowanceUsage(ctx context.Context, tx *sql.Tx, snapshot *PlacementTariffSnapshot, clusterIDs []string) (PlacementAllowanceUsage, error) {
	usage := PlacementAllowanceUsage{Status: PlacementUsageNotRequired}
	if !placementNeedsAllowanceUsage(snapshot) {
		return usage, nil
	}
	start, end, current := snapshot.AllowancePeriod()
	if !current || end.Sub(start) > 366*24*time.Hour {
		usage.Status = PlacementUsageUnknownPeriod
		return usage, nil
	}
	usage.PeriodStart, usage.PeriodEnd, usage.Through = start, end, snapshot.AsOf.UTC().Truncate(5*time.Minute)
	if !usage.Through.After(start) {
		usage.Status = PlacementUsageNoClosedWindow
		return usage, nil
	}
	queries := purserdb.New(tx)
	coverage, err := queries.ReadPlacementUsageCoverage(ctx, purserdb.ReadPlacementUsageCoverageParams{TenantID: snapshot.TenantID, PeriodStart: start, Cutoff: usage.Through, ObservedAt: snapshot.AsOf})
	if err != nil {
		return PlacementAllowanceUsage{}, err
	}
	usage.ActiveSources, usage.MissingWindows, usage.OpenAnomalies = coverage.ActiveSources, coverage.MissingWindows, coverage.OpenAnomalies
	switch {
	case usage.ActiveSources < 0 || usage.MissingWindows < 0 || usage.OpenAnomalies < 0:
		return PlacementAllowanceUsage{}, fmt.Errorf("invalid placement metering coverage")
	case usage.ActiveSources == 0:
		usage.Status = PlacementUsageMissingSources
	case usage.MissingWindows > 0:
		usage.Status = PlacementUsageMissingWindows
	case usage.OpenAnomalies > 0:
		usage.Status = PlacementUsageOpenAnomalies
	default:
		usage.Status = PlacementUsageCovered
	}
	if usage.Status != PlacementUsageCovered {
		return usage, nil
	}
	rows, err := queries.ListPlacementAllowanceUsage(ctx, purserdb.ListPlacementAllowanceUsageParams{TenantID: snapshot.TenantID, ClusterIds: clusterIDs, PeriodStart: start, Cutoff: usage.Through})
	if err != nil {
		return PlacementAllowanceUsage{}, err
	}
	if len(rows) > (len(clusterIDs)+1)*3 {
		return PlacementAllowanceUsage{}, fmt.Errorf("placement usage exceeds meter/cluster bound")
	}
	totals := make(map[string]map[rating.Meter]decimal.Decimal, len(clusterIDs))
	for _, id := range clusterIDs {
		totals[id] = map[rating.Meter]decimal.Decimal{rating.MeterIngressGB: decimal.Zero, rating.MeterEgressGB: decimal.Zero, rating.MeterDeliveredMinutes: decimal.Zero}
	}
	seen := make(map[[2]string]bool, len(rows))
	for _, row := range rows {
		if row.ClusterID == "" {
			usage.Status = PlacementUsageUnattributed
			return usage, nil
		}
		meter := rating.Meter(row.UsageType)
		if totals[row.ClusterID] == nil || (meter != rating.MeterIngressGB && meter != rating.MeterEgressGB && meter != rating.MeterDeliveredMinutes) || seen[[2]string{row.ClusterID, row.UsageType}] {
			return PlacementAllowanceUsage{}, fmt.Errorf("invalid placement usage identity")
		}
		seen[[2]string{row.ClusterID, row.UsageType}] = true
		quantity, parseErr := decimal.NewFromString(row.Quantity)
		if parseErr != nil {
			return PlacementAllowanceUsage{}, fmt.Errorf("invalid placement usage quantity")
		}
		if row.InvalidEvidence || !placementDecimalBounded(quantity) || quantity.IsNegative() {
			usage.Status = PlacementUsageInvalid
			return usage, nil
		}
		totals[row.ClusterID][meter] = quantity
	}
	usage.Totals = totals
	return usage, nil
}
