package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"frameworks/api_billing/internal/appconfig"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// runFinancialAudit invokes the same GET-only reader as the operator RPC without
// starting a service or requiring a database upgrade to inspect an older release.
func runFinancialAudit(args []string) int {
	flags := flag.NewFlagSet("financial-audit", flag.ContinueOnError)
	output := flags.String("output", "", "private JSON evidence file (must not exist)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	cfg, cfgErr := config.Load[appconfig.PurserFinancialAudit](config.Options{Service: "purser"})
	if cfgErr != nil || *output == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "financial-audit requires STRIPE_SECRET_KEY and --output")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := billingstripe.NewClient(billingstripe.Config{SecretKey: cfg.StripeSecretKey, Logger: logging.NewLoggerWithService("financial-audit")})
	through := time.Now().UTC()
	records := map[string][]json.RawMessage{}
	for _, source := range []string{"stripe_account", "stripe_balance_snapshot", "stripe_tax_settings", "stripe_subscriptions", "stripe_customers", "stripe_invoices", "stripe_charges", "stripe_balance_transactions", "stripe_payouts"} {
		cursor := ""
		for {
			page, err := client.FinancialEvidencePage(ctx, source, cursor, time.Unix(0, 0), through)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s could not be read; no audit file published\n", source)
				return 1
			}
			for _, raw := range page.Records {
				records[source] = append(records[source], json.RawMessage(raw))
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	payload, err := json.MarshalIndent(map[string]any{"captured_through": through, "read_only": true, "posting_ready": false, "records": records}, "", "  ")
	if err != nil {
		return 1
	}
	f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create private audit output")
		return 1
	}
	_, err = f.Write(payload)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		fmt.Fprintln(os.Stderr, "audit output write failed")
		return 1
	}
	fmt.Fprintln(os.Stdout, "Stripe financial evidence saved (GET requests only)")
	return 0
}
