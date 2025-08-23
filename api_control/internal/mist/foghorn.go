package mist

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// FoghornClient handles interactions with the Foghorn load balancer
type FoghornClient struct {
	BaseURL  string
	Username string
	Password string
	Client   *http.Client
}

// NewFoghornClient creates a new Foghorn client
func NewFoghornClient() *FoghornClient {
	return &FoghornClient{
		BaseURL:  os.Getenv("FOGHORN_URL"),
		Username: os.Getenv("MIST_API_USERNAME"),
		Password: os.Getenv("MIST_API_PASSWORD"),
		Client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// DiscoverStreamNode queries Foghorn to find which MistServer node has the stream
func (f *FoghornClient) DiscoverStreamNode(streamName string) (string, error) {
	if f.BaseURL == "" {
		return "", fmt.Errorf("FOGHORN_URL not configured")
	}

	// URL encode the stream name
	encodedStream := url.QueryEscape(streamName)
	foghornURL := fmt.Sprintf("%s/?source=%s", f.BaseURL, encodedStream)

	resp, err := f.Client.Get(foghornURL)
	if err != nil {
		return "", fmt.Errorf("failed to query Foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Foghorn returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read Foghorn response: %w", err)
	}

	content := strings.TrimSpace(string(body))

	// Parse response - expecting format like "dtsc://mist-mumbai.stronk.rocks"
	if strings.HasPrefix(content, "dtsc://") {
		nodeHost := content[7:] // Remove "dtsc://" prefix
		nodeURL := fmt.Sprintf("https://%s", nodeHost)
		return nodeURL, nil
	}

	return "", fmt.Errorf("unexpected Foghorn response: %s", content)
}

// MakeNodeRequest makes an authenticated request to a specific MistServer node
func (f *FoghornClient) MakeNodeRequest(nodeURL, path string) (*http.Response, error) {
	fullURL := fmt.Sprintf("%s%s", nodeURL, path)

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add basic auth if credentials are available
	if f.Username != "" && f.Password != "" {
		req.SetBasicAuth(f.Username, f.Password)
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request to %s: %w", fullURL, err)
	}

	return resp, nil
}
