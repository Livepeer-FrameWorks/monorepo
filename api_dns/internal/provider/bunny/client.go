package bunny

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"frameworks/pkg/clients"

	"github.com/failsafe-go/failsafe-go"
)

const (
	defaultBaseURL = "https://api.bunny.net"
	defaultTimeout = 30 * time.Second
)

type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	executor   failsafe.Executor[*http.Response]
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		executor: clients.NewHTTPExecutor(clients.DefaultHTTPExecutorConfig()), //nolint:bodyclose
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var reqBodyBytes []byte
	if body != nil {
		var err error
		reqBodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	requestURL := c.baseURL + path
	execute := func() (*http.Response, error) {
		var reqBody io.Reader
		if reqBodyBytes != nil {
			reqBody = bytes.NewReader(reqBodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, requestURL, reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("AccessKey", c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		return c.httpClient.Do(req)
	}

	executor := c.executor
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete {
		cfg := clients.DefaultHTTPExecutorConfig()
		cfg.MaxRetries = 0
		executor = clients.NewHTTPExecutor(cfg) //nolint:bodyclose
	}
	if executor == nil {
		executor = clients.NewHTTPExecutor(clients.DefaultHTTPExecutorConfig()) //nolint:bodyclose
	}

	resp, err := clients.ExecuteHTTP(ctx, executor, execute)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bunny API request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to parse response: %w (body: %s)", err, string(respBody))
	}
	return nil
}

func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	page := 1
	for {
		var resp listZonesResponse
		path := fmt.Sprintf("/dnszone?page=%d&perPage=1000", page)
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if !resp.HasMoreItems {
			break
		}
		page++
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Domain < all[j].Domain })
	return all, nil
}

func (c *Client) AddZone(ctx context.Context, domain string) (*Zone, error) {
	var zone Zone
	body := map[string]string{"Domain": strings.TrimSuffix(strings.TrimSpace(domain), ".")}
	if err := c.doRequest(ctx, http.MethodPost, "/dnszone", body, &zone); err != nil {
		return nil, err
	}
	return &zone, nil
}

func (c *Client) EnsureZone(ctx context.Context, domain string) (*Zone, bool, error) {
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil, false, fmt.Errorf("zone domain is required")
	}
	if zone, ok, err := c.FindZone(ctx, domain); err != nil || ok {
		return zone, false, err
	}
	zone, err := c.AddZone(ctx, domain)
	return zone, err == nil, err
}

func (c *Client) FindZone(ctx context.Context, domain string) (*Zone, bool, error) {
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil, false, fmt.Errorf("zone domain is required")
	}
	zones, err := c.ListZones(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, zone := range zones {
		if normalizeDomain(zone.Domain) == domain {
			return &zone, true, nil
		}
	}
	return nil, false, nil
}

func (c *Client) ListRecords(ctx context.Context, zoneID int64) ([]Record, error) {
	var zone Zone
	if err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/dnszone/%d", zoneID), nil, &zone); err != nil {
		return nil, err
	}
	return zone.Records, nil
}

func (c *Client) AddRecord(ctx context.Context, zoneID int64, record Record) (*Record, error) {
	var created Record
	if err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/dnszone/%d/records", zoneID), record, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *Client) UpdateRecord(ctx context.Context, zoneID, recordID int64, record Record) error {
	return c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/dnszone/%d/records/%d", zoneID, recordID), record, nil)
}

func (c *Client) DeleteRecord(ctx context.Context, zoneID, recordID int64) error {
	return c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/dnszone/%d/records/%d", zoneID, recordID), nil, nil)
}

func (c *Client) ReconcileRecordSet(ctx context.Context, zoneID int64, name string, recordType int, desired []Record) error {
	name = normalizeRecordName(name)
	current, err := c.ListRecords(ctx, zoneID)
	if err != nil {
		return err
	}

	var matching []Record
	for _, record := range current {
		if record.Type == recordType && normalizeRecordName(record.Name) == name {
			matching = append(matching, record)
		}
	}

	sortRecords(desired)
	sortRecords(matching)

	used := make(map[int64]bool, len(matching))
	for _, want := range desired {
		want.Name = name
		want.Type = recordType
		found := false
		for _, have := range matching {
			if used[have.ID] || !sameRecordIdentity(have, want) {
				continue
			}
			found = true
			used[have.ID] = true
			if !sameRecordConfig(have, want) {
				want.ID = have.ID
				if err := c.UpdateRecord(ctx, zoneID, have.ID, want); err != nil {
					return err
				}
			}
			break
		}
		if !found {
			if _, err := c.AddRecord(ctx, zoneID, want); err != nil {
				return err
			}
		}
	}

	for _, have := range matching {
		if used[have.ID] {
			continue
		}
		if err := c.DeleteRecord(ctx, zoneID, have.ID); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
}

func normalizeRecordName(name string) string {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || name == "@" {
		return ""
	}
	return name
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].Value != records[j].Value {
			return records[i].Value < records[j].Value
		}
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].ID < records[j].ID
	})
}

func sameRecordIdentity(a, b Record) bool {
	return normalizeRecordName(a.Name) == normalizeRecordName(b.Name) &&
		a.Type == b.Type &&
		a.Value == b.Value
}

func sameRecordConfig(a, b Record) bool {
	return sameRecordIdentity(a, b) &&
		a.TTL == b.TTL &&
		a.Weight == b.Weight &&
		a.MonitorType == b.MonitorType &&
		a.SmartRoutingType == b.SmartRoutingType &&
		floatPtrEqual(a.GeolocationLatitude, b.GeolocationLatitude) &&
		floatPtrEqual(a.GeolocationLongitude, b.GeolocationLongitude) &&
		a.Disabled == b.Disabled &&
		a.Comment == b.Comment
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
