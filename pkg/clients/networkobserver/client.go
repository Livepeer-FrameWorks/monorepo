package networkobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

// InternalServerName is the identity in the observer's internal-CA
// certificate. Mesh routes such as network-observer.<region>.internal are
// addresses, so a client verifying the internal CA checks this name instead.
const InternalServerName = "network-observer.internal"

// MaxBodyBytes bounds every request and response body on the observer API,
// matching the observer's own ingest limit.
const MaxBodyBytes = 8 << 20

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
	Observers   []obs.Observer `json:"observers"`
}

// TLS selects how the client verifies an https observer. An empty CAFile uses
// the system roots, as for a public certificate; a CAFile adds the internal CA
// and verifies InternalServerName unless ServerName names another identity.
type TLS struct {
	CAFile     string
	ServerName string
}

func New(baseURL, secret string, allowHTTP bool) (*Client, error) {
	return NewWithTLS(baseURL, secret, allowHTTP, TLS{})
}

// NewWithTLS builds a client whose https connections verify the given CA.
// Plain http is accepted only when allowHTTP is set.
func NewWithTLS(baseURL, secret string, allowHTTP bool, verify TLS) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (!allowHTTP || u.Scheme != "http")) {
		return nil, fmt.Errorf("invalid observer URL; verified HTTPS required")
	}
	if secret == "" {
		return nil, fmt.Errorf("observer secret required")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, MaxIdleConns: 16, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second}
	if u.Scheme == "https" && strings.TrimSpace(verify.CAFile) != "" {
		tlsConfig, tlsErr := grpcutil.HTTPClientTLSConfig(grpcutil.ClientTLSConfig{
			CACertFile:        verify.CAFile,
			ServerName:        verify.ServerName,
			DefaultServerName: InternalServerName,
		})
		if tlsErr != nil {
			return nil, fmt.Errorf("observer TLS: %w", tlsErr)
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Secret: secret, HTTP: &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
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

// RelayResponse is the observer's answer to a relayed request.
type RelayResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// Relay forwards one external prober request to the observer. The prober's own
// Authorization header is passed through so the observer authenticates the
// prober, not the relay; clientIP replaces any X-Forwarded-For the prober sent
// so the observer trusts only the relay's view of the source address.
func (c *Client) Relay(ctx context.Context, method, path, rawQuery, authorization, clientIP string, body io.Reader) (*RelayResponse, error) {
	target := c.BaseURL + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json")
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBodyBytes {
		return nil, fmt.Errorf("observer response exceeds %d bytes", MaxBodyBytes)
	}
	return &RelayResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: raw}, nil
}
