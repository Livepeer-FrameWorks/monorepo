package fx

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Feed is one of the ECB euro foreign exchange reference rate XML files.
type Feed string

const (
	// FeedDaily holds the latest reference date.
	FeedDaily Feed = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"
	// Feed90Days holds the last 90 days of reference dates.
	Feed90Days Feed = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-hist-90d.xml"
	// FeedHistory holds every reference date since 1999.
	FeedHistory Feed = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-hist.xml"
)

const (
	maxFeedBytes = 64 << 20
	fetchTimeout = 60 * time.Second
)

// Fetcher returns the body of an ECB feed. Tests substitute fixtures.
type Fetcher func(ctx context.Context, feed Feed) ([]byte, error)

// HTTPFetcher fetches feeds over HTTP with client.
func HTTPFetcher(client *http.Client) Fetcher {
	return func(ctx context.Context, feed Feed) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(feed), nil)
		if err != nil {
			return nil, fmt.Errorf("build ECB request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", feed, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: status %d", feed, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", feed, err)
		}
		if len(body) > maxFeedBytes {
			return nil, fmt.Errorf("read %s: body exceeds %d bytes", feed, maxFeedBytes)
		}
		return body, nil
	}
}

// FetchRates fetches and parses one feed.
func FetchRates(ctx context.Context, fetch Fetcher, feed Feed) ([]Rate, error) {
	if fetch == nil {
		return nil, errors.New("fx: no ECB fetcher configured")
	}
	body, err := fetch(ctx, feed)
	if err != nil {
		return nil, err
	}
	rates, err := ParseECB(body)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", feed, err)
	}
	return rates, nil
}

type ecbEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Days    []struct {
		Time  string `xml:"time,attr"`
		Rates []struct {
			Currency string `xml:"currency,attr"`
			Rate     string `xml:"rate,attr"`
		} `xml:"Cube"`
	} `xml:"Cube>Cube"`
}

// ParseECB parses an ECB reference rate XML document into the USD and GBP
// rates of every reference date it lists, ordered by currency and date. Dates
// the ECB did not publish, such as weekends and TARGET holidays, are absent.
// A listed reference date without a positive USD and GBP rate is an error.
func ParseECB(body []byte) ([]Rate, error) {
	var envelope ecbEnvelope
	if err := xml.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode ECB XML: %w", err)
	}
	if len(envelope.Days) == 0 {
		return nil, errors.New("ECB document lists no reference dates")
	}
	seen := make(map[string]bool)
	var rates []Rate
	for _, day := range envelope.Days {
		referenceDate, err := time.Parse(time.DateOnly, strings.TrimSpace(day.Time))
		if err != nil {
			return nil, fmt.Errorf("reference date %q: %w", day.Time, err)
		}
		found := make(map[string]bool, len(Currencies))
		for _, quote := range day.Rates {
			currency := strings.ToUpper(strings.TrimSpace(quote.Currency))
			if currency != USD && currency != GBP {
				continue
			}
			units, err := decimal.NewFromString(strings.TrimSpace(quote.Rate))
			if err != nil {
				return nil, fmt.Errorf("%s rate on %s: %w", currency, day.Time, err)
			}
			if !units.IsPositive() {
				return nil, fmt.Errorf("%s rate on %s is not positive: %s", currency, day.Time, units)
			}
			key := currency + "|" + referenceDate.Format(time.DateOnly)
			if seen[key] {
				return nil, fmt.Errorf("duplicate %s rate on %s", currency, day.Time)
			}
			seen[key] = true
			found[currency] = true
			rates = append(rates, Rate{Currency: currency, ReferenceDate: referenceDate, UnitsPerEUR: units, Source: SourceECB})
		}
		for _, currency := range Currencies {
			if !found[currency] {
				return nil, fmt.Errorf("reference date %s has no %s rate", day.Time, currency)
			}
		}
	}
	sort.Slice(rates, func(i, j int) bool {
		if rates[i].Currency != rates[j].Currency {
			return rates[i].Currency < rates[j].Currency
		}
		return rates[i].ReferenceDate.Before(rates[j].ReferenceDate)
	})
	return rates, nil
}
