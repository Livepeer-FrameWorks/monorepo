package listmonk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"frameworks/pkg/clients"

	"github.com/failsafe-go/failsafe-go"
)

type APIError struct {
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("listmonk returned status: %d", e.StatusCode)
}

type Client struct {
	baseURL      string
	username     string
	password     string
	client       *http.Client
	httpExecutor failsafe.Executor[*http.Response]
	shouldRetry  func(resp *http.Response, err error) bool
}

type Option func(*Client)

func NewClient(baseURL, username, password string, opts ...Option) *Client {
	defaultConfig := clients.DefaultHTTPExecutorConfig()
	c := &Client{
		baseURL:      baseURL,
		username:     username,
		password:     password,
		client:       &http.Client{Timeout: 10 * time.Second},
		httpExecutor: clients.NewHTTPExecutor(defaultConfig),
		shouldRetry:  defaultConfig.ShouldRetry,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.client = httpClient
		}
	}
}

func WithHTTPExecutorConfig(cfg clients.HTTPExecutorConfig) Option {
	return func(c *Client) {
		c.httpExecutor = clients.NewHTTPExecutor(cfg)
		c.shouldRetry = cfg.ShouldRetry
	}
}

func WithHTTPExecutor(executor failsafe.Executor[*http.Response], shouldRetry func(resp *http.Response, err error) bool) Option {
	return func(c *Client) {
		if executor != nil {
			c.httpExecutor = executor
			c.shouldRetry = shouldRetry
		}
	}
}

func (c *Client) doRequest(ctx context.Context, build func(ctx context.Context) (*http.Request, error)) (*http.Response, error) {
	if c.httpExecutor == nil {
		req, err := build(ctx)
		if err != nil {
			return nil, err
		}
		return c.client.Do(req)
	}

	return clients.ExecuteHTTP(ctx, c.httpExecutor, func() (*http.Response, error) {
		req, err := build(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := c.client.Do(req)
		if c.shouldRetry != nil && c.shouldRetry(resp, err) {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		return resp, err
	})
}

type SubscriberRequest struct {
	Email      string                 `json:"email"`
	Name       string                 `json:"name"`
	Status     string                 `json:"status"`
	Lists      []int                  `json:"lists"`
	Attribs    map[string]interface{} `json:"attribs"`
	Preconfirm bool                   `json:"preconfirm"` // false = trigger double opt-in
}

// Subscribe adds a subscriber. If preconfirm is false, it triggers double opt-in (if enabled in Listmonk).
func (c *Client) Subscribe(ctx context.Context, email, name string, listID int, preconfirm bool) error {
	url := fmt.Sprintf("%s/api/subscribers", c.baseURL)

	reqBody := SubscriberRequest{
		Email:      email,
		Name:       name,
		Status:     "enabled",
		Lists:      []int{listID},
		Preconfirm: preconfirm,
		Attribs:    map[string]interface{}{"source": "api"},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := c.doRequest(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth(c.username, c.password)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return &APIError{StatusCode: resp.StatusCode}
	}

	return nil
}

// Blocklist marks a subscriber as blocklisted (unsubscribed from everything)
func (c *Client) Blocklist(ctx context.Context, email string) error {
	url := fmt.Sprintf("%s/api/subscribers", c.baseURL)

	reqBody := SubscriberRequest{
		Email:  email,
		Status: "blocklisted",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := c.doRequest(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth(c.username, c.password)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return &APIError{StatusCode: resp.StatusCode}
	}

	return nil
}

// SubscriberInfo contains subscriber details from Listmonk
type SubscriberInfo struct {
	ID     int    // Listmonk internal subscriber ID
	Status string // "enabled" or "blocklisted"
	Lists  []ListSubscription
}

// ListSubscription represents a subscriber's membership in a list
type ListSubscription struct {
	ListID int
	Status string // "confirmed", "unconfirmed", "unsubscribed"
}

// GetSubscriber returns subscriber info for an email address.
// Returns (info, exists, error). If subscriber doesn't exist, exists=false.
func (c *Client) GetSubscriber(ctx context.Context, email string) (*SubscriberInfo, bool, error) {
	// Escape single quotes in email and URL-encode the query parameter
	escapedEmail := strings.ReplaceAll(email, "'", "''")
	query := fmt.Sprintf("subscribers.email='%s'", escapedEmail)
	reqURL := fmt.Sprintf("%s/api/subscribers?query=%s", c.baseURL, url.QueryEscape(query))

	resp, err := c.doRequest(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth(c.username, c.password)
		return req, nil
	})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, false, &APIError{StatusCode: resp.StatusCode}
	}

	var result struct {
		Data struct {
			Results []struct {
				ID     int    `json:"id"`
				Status string `json:"status"`
				Lists  []struct {
					ID                 int    `json:"id"`
					SubscriptionStatus string `json:"subscription_status"`
				} `json:"lists"`
			} `json:"results"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Data.Results) == 0 {
		return nil, false, nil
	}

	sub := result.Data.Results[0]
	info := &SubscriberInfo{
		ID:     sub.ID,
		Status: sub.Status,
	}
	for _, l := range sub.Lists {
		info.Lists = append(info.Lists, ListSubscription{
			ListID: l.ID,
			Status: l.SubscriptionStatus,
		})
	}

	return info, true, nil
}

// Unsubscribe removes a subscriber from a specific list (per-list unsubscribe).
// This does NOT blocklist the subscriber - they can still receive emails from other lists.
func (c *Client) Unsubscribe(ctx context.Context, subscriberID int, listID int) error {
	reqURL := fmt.Sprintf("%s/api/subscribers/lists", c.baseURL)

	reqBody := struct {
		IDs           []int  `json:"ids"`
		Action        string `json:"action"`
		TargetListIDs []int  `json:"target_list_ids"`
	}{
		IDs:           []int{subscriberID},
		Action:        "unsubscribe",
		TargetListIDs: []int{listID},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := c.doRequest(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "PUT", reqURL, bytes.NewBuffer(jsonBody))
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth(c.username, c.password)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("listmonk returned status: %d", resp.StatusCode)
	}

	return nil
}

// IsSubscribedToList checks if a subscriber is subscribed to a specific list.
func (info *SubscriberInfo) IsSubscribedToList(listID int) bool {
	if info == nil {
		return false
	}
	// If subscriber is blocklisted globally, they're effectively unsubscribed
	if info.Status == "blocklisted" {
		return false
	}
	for _, l := range info.Lists {
		if l.ListID == listID && l.Status == "confirmed" {
			return true
		}
	}
	return false
}
