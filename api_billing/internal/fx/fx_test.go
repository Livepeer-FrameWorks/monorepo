package fx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func day(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func usd(units string, date string) Rate {
	return Rate{Currency: USD, ReferenceDate: day(date), UnitsPerEUR: decimal.RequireFromString(units), Source: SourceECB}
}

func TestParseECBDailyFeed(t *testing.T) {
	rates, err := ParseECB(readFixture(t, "eurofxref-daily.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rates) != 2 {
		t.Fatalf("rates = %+v, want only GBP and USD", rates)
	}
	if rates[0].Currency != GBP || rates[0].UnitsPerEUR.String() != "0.85445" || !rates[0].ReferenceDate.Equal(day("2026-04-08")) {
		t.Fatalf("GBP rate = %+v", rates[0])
	}
	if rates[1].Currency != USD || rates[1].UnitsPerEUR.String() != "1.0921" || rates[1].Source != SourceECB {
		t.Fatalf("USD rate = %+v", rates[1])
	}
}

// The 90-day fixture spans Easter 2026: the ECB published nothing from Good
// Friday 3 April through Easter Monday 6 April.
func TestParseECB90DayFeedKeepsHolidayGap(t *testing.T) {
	rates, err := ParseECB(readFixture(t, "eurofxref-hist-90d.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var usdDates []string
	for _, rate := range rates {
		if rate.Currency == USD {
			usdDates = append(usdDates, rate.ReferenceDate.Format(time.DateOnly))
		}
	}
	want := "2026-03-27,2026-03-30,2026-03-31,2026-04-01,2026-04-02,2026-04-07,2026-04-08"
	if got := strings.Join(usdDates, ","); got != want {
		t.Fatalf("USD reference dates = %s, want %s", got, want)
	}
	if len(rates) != 14 {
		t.Fatalf("rates = %d, want 7 USD and 7 GBP", len(rates))
	}
}

func TestParseECBRefusesIncompleteOrMalformedDocuments(t *testing.T) {
	cases := map[string]string{
		"not xml":         `rates`,
		"no dates":        `<Envelope><Cube></Cube></Envelope>`,
		"missing GBP":     `<Envelope><Cube><Cube time="2026-04-08"><Cube currency="USD" rate="1.09"/></Cube></Cube></Envelope>`,
		"zero rate":       `<Envelope><Cube><Cube time="2026-04-08"><Cube currency="USD" rate="0"/><Cube currency="GBP" rate="0.85"/></Cube></Cube></Envelope>`,
		"bad rate":        `<Envelope><Cube><Cube time="2026-04-08"><Cube currency="USD" rate="1,09"/><Cube currency="GBP" rate="0.85"/></Cube></Cube></Envelope>`,
		"bad date":        `<Envelope><Cube><Cube time="08-04-2026"><Cube currency="USD" rate="1.09"/><Cube currency="GBP" rate="0.85"/></Cube></Cube></Envelope>`,
		"duplicate quote": `<Envelope><Cube><Cube time="2026-04-08"><Cube currency="USD" rate="1.09"/><Cube currency="USD" rate="1.08"/><Cube currency="GBP" rate="0.85"/></Cube></Cube></Envelope>`,
	}
	for name, body := range cases {
		if _, err := ParseECB([]byte(body)); err == nil {
			t.Errorf("%s: parse succeeded, want error", name)
		}
	}
}

func TestFetchRatesUsesInjectedFetcher(t *testing.T) {
	var requested Feed
	rates, err := FetchRates(context.Background(), func(_ context.Context, feed Feed) ([]byte, error) {
		requested = feed
		return readFixture(t, "eurofxref-daily.xml"), nil
	}, FeedDaily)
	if err != nil || len(rates) != 2 || requested != FeedDaily {
		t.Fatalf("rates=%v err=%v feed=%s", rates, err, requested)
	}
	if _, err := FetchRates(context.Background(), func(context.Context, Feed) ([]byte, error) {
		return nil, errors.New("offline")
	}, FeedDaily); err == nil {
		t.Fatal("fetch failure must surface")
	}
	if _, err := FetchRates(context.Background(), nil, FeedDaily); err == nil {
		t.Fatal("missing fetcher must fail")
	}
}

func TestConversionsRoundHalfAwayFromZero(t *testing.T) {
	two := usd("2", "2026-04-08")
	cases := []struct {
		name string
		got  func() (int64, error)
		want int64
	}{
		{"to EUR half up", func() (int64, error) { return ToEUR(3, two) }, 2},
		{"to EUR negative half away", func() (int64, error) { return ToEUR(-3, two) }, -2},
		{"to EUR below half", func() (int64, error) { return ToEUR(1, usd("3", "2026-04-08")) }, 0},
		{"to EUR at ECB rate", func() (int64, error) { return ToEUR(2500, usd("1.0921", "2026-04-08")) }, 2289},
		{"from EUR half", func() (int64, error) { return FromEUR(5, usd("1.5", "2026-04-08")) }, 8},
		{"from EUR negative half", func() (int64, error) { return FromEUR(-5, usd("1.5", "2026-04-08")) }, -8},
		{"from EUR at ECB rate", func() (int64, error) { return FromEUR(10000, usd("1.0921", "2026-04-08")) }, 10921},
		{"identity", func() (int64, error) { return ToEUR(1234, Identity(day("2026-04-08"))) }, 1234},
	}
	for _, tc := range cases {
		got, err := tc.got()
		if err != nil || got != tc.want {
			t.Errorf("%s = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

func TestPartsThatCompleteTheOriginalSumToTheRecordedEUR(t *testing.T) {
	record, err := RecordToEUR(1000, usd("1.1669", "2026-04-08"))
	if err != nil || record.EURMinor != 857 {
		t.Fatalf("record = %+v, %v; want 857 EUR cents", record, err)
	}
	var priorOriginal, priorEUR int64
	var eur []int64
	for _, originalPart := range []int64{333, 333, 334} {
		part, partErr := record.Part(originalPart, priorOriginal, priorEUR)
		if partErr != nil {
			t.Fatal(partErr)
		}
		eur = append(eur, part.EURMinor)
		priorOriginal += part.OriginalMinor
		priorEUR += part.EURMinor
	}
	// Cumulative rounding allocates 285, then brings 666/1000 to 571 cents.
	if eur[0] != 285 || eur[1] != 286 || eur[2] != 286 || priorEUR != record.EURMinor {
		t.Fatalf("parts = %v summing to %d, want [285 286 286] summing to %d", eur, priorEUR, record.EURMinor)
	}
	identity, err := RecordToEUR(1000, Identity(day("2026-04-08")))
	if err != nil {
		t.Fatal(err)
	}
	if part, err := identity.Part(400, 600, 600); err != nil || part.EURMinor != 400 {
		t.Fatalf("identity part = %+v, %v; want 400", part, err)
	}
}

func TestSmallPartsNeverOverdrawTheRecordedEUR(t *testing.T) {
	for original := int64(1); original <= 30; original++ {
		for eur := int64(0); eur <= 30; eur++ {
			record := Record{OriginalMinor: original, EURMinor: eur, Source: SourceECB}
			var allocated int64
			for prior := int64(0); prior < original; prior++ {
				part, err := record.Part(1, prior, allocated)
				if err != nil || part.EURMinor < 0 || allocated+part.EURMinor > eur {
					t.Fatalf("%d/%d part %d: %+v, %v", original, eur, prior, part, err)
				}
				allocated += part.EURMinor
			}
			if allocated != eur {
				t.Fatalf("allocated %d, want %d", allocated, eur)
			}
		}
	}
}

func TestCrossRequiresOneReferenceDate(t *testing.T) {
	gbp := Rate{Currency: GBP, ReferenceDate: day("2026-04-08"), UnitsPerEUR: decimal.RequireFromString("0.85445"), Source: SourceECB}
	got, err := Cross(10000, gbp, usd("1.0921", "2026-04-08"))
	if err != nil {
		t.Fatal(err)
	}
	// 10000 GBP cents * 1.0921 / 0.85445 = 12781.32...
	if got != 12781 {
		t.Fatalf("GBP to USD = %d, want 12781", got)
	}
	if got, err := Cross(10000, Identity(day("2026-04-08")), usd("1.0921", "2026-04-08")); err != nil || got != 10921 {
		t.Fatalf("EUR to USD = %d, %v", got, err)
	}
	if _, err := Cross(10000, gbp, usd("1.0893", "2026-04-07")); !errors.Is(err, ErrReferenceDateMismatch) {
		t.Fatalf("cross-date conversion err = %v, want ErrReferenceDateMismatch", err)
	}
}

func TestConversionsRefuseInvalidRates(t *testing.T) {
	if _, err := ToEUR(100, usd("0", "2026-04-08")); err == nil {
		t.Fatal("zero rate must be refused")
	}
	if _, err := ToEUR(100, Rate{Currency: "JPY", UnitsPerEUR: decimal.NewFromInt(160)}); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("JPY err = %v", err)
	}
	if _, err := FromEUR(100, Rate{Currency: EUR, UnitsPerEUR: decimal.NewFromInt(2)}); err == nil {
		t.Fatal("EUR rate other than 1 must be refused")
	}
}

func TestAgeDaysUsesCalendarDates(t *testing.T) {
	reference := day("2026-04-02")
	if got := AgeDays(reference, time.Date(2026, 4, 7, 23, 59, 0, 0, time.UTC)); got != 5 {
		t.Fatalf("age = %d, want 5", got)
	}
	if got := AgeDays(reference, time.Date(2026, 4, 8, 0, 0, 1, 0, time.UTC)); got != 6 {
		t.Fatalf("age = %d, want 6", got)
	}
}
