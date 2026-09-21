package billing

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

func TestPresentmentCurrencyForCountry(t *testing.T) {
	cases := map[string]string{
		"NL": "EUR", "de": "EUR", " fr ": "EUR", "NO": "EUR", "IS": "EUR", "LI": "EUR", "GR": "EUR",
		"GB": "GBP", "gb": "GBP",
		"US": "USD", "CH": "USD", "JP": "USD", "": "USD", "EL": "USD", "UK": "USD",
	}
	for country, want := range cases {
		if got := PresentmentCurrencyForCountry(country); got != want {
			t.Errorf("PresentmentCurrencyForCountry(%q) = %q, want %q", country, got, want)
		}
	}
	if len(eurPresentmentCountries) != 30 {
		t.Fatalf("EUR presentment countries = %d, want the 27 EU member states plus IS, LI, NO", len(eurPresentmentCountries))
	}
}

// The postdeploy migration derives existing tenants' presentment currency in
// SQL; it must classify every country exactly like the Go mapping.
func TestPresentmentCurrencyPostdeployMatchesGoMapping(t *testing.T) {
	content, err := dbsql.Content.ReadFile("migrations/purser/v0.3.11/postdeploy/002_derive_presentment_currency.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(content)
	eurBlock := regexp.MustCompile(`(?s)IN \(([^)]*)\)\s*THEN 'EUR'`).FindStringSubmatch(sqlText)
	if eurBlock == nil {
		t.Fatal("postdeploy migration has no EUR country list")
	}
	var sqlCodes []string
	for _, match := range regexp.MustCompile(`'([A-Z]{2})'`).FindAllStringSubmatch(eurBlock[1], -1) {
		sqlCodes = append(sqlCodes, match[1])
	}
	var goCodes []string
	for code := range eurPresentmentCountries {
		goCodes = append(goCodes, code)
	}
	sort.Strings(sqlCodes)
	sort.Strings(goCodes)
	if strings.Join(sqlCodes, ",") != strings.Join(goCodes, ",") {
		t.Fatalf("EUR countries differ:\nSQL: %v\nGo:  %v", sqlCodes, goCodes)
	}
	gbp := regexp.MustCompile(`= '([A-Z]{2})' THEN 'GBP'`).FindStringSubmatch(sqlText)
	if gbp == nil || PresentmentCurrencyForCountry(gbp[1]) != PresentmentGBP {
		t.Fatalf("postdeploy GBP country %v does not map to GBP in Go", gbp)
	}
	if !strings.Contains(sqlText, "ELSE 'USD'") || PresentmentCurrencyForCountry("") != PresentmentUSD {
		t.Fatal("postdeploy and Go must both present every other country in USD")
	}
}
