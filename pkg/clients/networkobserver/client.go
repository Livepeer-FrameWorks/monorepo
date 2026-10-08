package networkobserver

import (
	"context"
	"encoding/json"
	"fmt"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL, Secret string
	HTTP            *http.Client
}
type Inventory struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Provider    string         `json:"provider"`
	Network     string         `json:"network"`
	Environment string         `json:"environment"`
	Resources   []obs.Resource `json:"resources"`
	Endpoints   []obs.Endpoint `json:"endpoints"`
}

func New(baseURL, secret string, allowHTTP bool) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (!allowHTTP || u.Scheme != "http")) {
		return nil, fmt.Errorf("invalid observer URL; verified HTTPS required")
	}
	if secret == "" {
		return nil, fmt.Errorf("observer secret required")
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Secret: secret, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("observer returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(out)
}
func (c *Client) Inventory(ctx context.Context, inactive bool) (*Inventory, error) {
	var out Inventory
	err := c.get(ctx, fmt.Sprintf("/v1/inventory?include_inactive=%t", inactive), &out)
	return &out, err
}
func (c *Client) Candidates(ctx context.Context) (*obs.CandidateFeed, error) {
	var out obs.CandidateFeed
	err := c.get(ctx, "/v1/candidates", &out)
	return &out, err
}
