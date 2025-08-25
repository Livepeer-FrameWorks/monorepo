package mist

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"frameworks/pkg/logging"
)

// Client handles interactions with MistServer API
type Client struct {
	BaseURL         string
	Username        string
	Password        string
	MetricsPassword string // Preshared secret for metrics endpoints (/{secret} and /{secret}.json)
	httpClient      *http.Client
	Logger          logging.Logger

	// Authentication state for TCP API
	authenticated bool
	authCookie    *http.Cookie
}

// PushInfo represents a push entry from push_list
type PushInfo struct {
	ID         int                    `json:"id"`
	StreamName string                 `json:"stream_name"`
	TargetURI  string                 `json:"target_uri"`
	ActualURI  string                 `json:"actual_uri"`
	Logs       []string               `json:"logs"`
	Status     map[string]interface{} `json:"status"`
}

// StreamInfo represents stream information
type StreamInfo struct {
	Name     string                 `json:"name"`
	Source   string                 `json:"source"`
	Active   bool                   `json:"active"`
	Metadata map[string]interface{} `json:"metadata"`
}

// NewClient creates a new MistServer API client
func NewClient(logger logging.Logger) *Client {
	metricsPassword := os.Getenv("MIST_PASSWORD")
	if metricsPassword == "" {
		metricsPassword = "koekjes" // Default dev secret
	}

	user := os.Getenv("MIST_API_USERNAME")
	if user == "" {
		user = "test"
	}

	pass := os.Getenv("MIST_API_PASSWORD")
	if pass == "" {
		pass = "test"
	}

	return &Client{
		BaseURL:         os.Getenv("MIST_API_URL"), // e.g., "http://localhost:4242"
		Username:        user,
		Password:        pass,
		MetricsPassword: metricsPassword,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		Logger:        logger,
		authenticated: false,
	}
}

// makeAPIRequest makes an authenticated request to MistServer TCP API
func (c *Client) makeAPIRequest(command map[string]interface{}) (map[string]interface{}, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("MIST_API_URL not configured")
	}

	// Ensure we're authenticated first
	if !c.authenticated {
		if err := c.authenticate(); err != nil {
			return nil, fmt.Errorf("authentication failed: %w", err)
		}
	}

	return c.callAPI(command)
}

// callAPI performs a single API call without triggering authenticate()
func (c *Client) callAPI(command map[string]interface{}) (map[string]interface{}, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal command: %w", err)
	}
	u := fmt.Sprintf("%s/api2?command=%s", base, url.QueryEscape(string(commandJSON)))

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	// Add session cookie if we have one
	if c.authCookie != nil {
		req.AddCookie(c.authCookie)
	}

	c.Logger.WithFields(logging.Fields{
		"url":     u,
		"command": string(commandJSON),
	}).Debug("Calling MistServer API")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(b))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	c.Logger.WithFields(logging.Fields{
		"response": string(body)[:min(200, len(body))],
	}).Debug("MistServer API response")

	return result, nil
}

// PushStart starts a new push from source stream to target URI
func (c *Client) PushStart(streamName, targetURI string) error {
	command := map[string]interface{}{
		"push_start": map[string]interface{}{
			"stream": streamName,
			"target": targetURI,
		},
	}

	_, err := c.makeAPIRequest(command)
	if err != nil {
		return fmt.Errorf("push_start failed: %w", err)
	}

	c.Logger.WithFields(logging.Fields{
		"stream": streamName,
		"target": targetURI,
	}).Info("Started MistServer push")

	return nil
}

// PushList returns list of currently active pushes
func (c *Client) PushList() ([]PushInfo, error) {
	command := map[string]interface{}{
		"push_list": true,
	}

	response, err := c.makeAPIRequest(command)
	if err != nil {
		return nil, fmt.Errorf("push_list failed: %w", err)
	}

	// Parse push_list response
	pushListRaw, exists := response["push_list"]
	if !exists {
		return []PushInfo{}, nil
	}

	pushListArray, ok := pushListRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected push_list format")
	}

	var pushes []PushInfo
	for _, pushRaw := range pushListArray {
		pushArray, ok := pushRaw.([]interface{})
		if !ok || len(pushArray) < 4 {
			continue // Skip malformed entries
		}

		push := PushInfo{}

		// Parse ID
		if id, ok := pushArray[0].(float64); ok {
			push.ID = int(id)
		}

		// Parse StreamName
		if streamName, ok := pushArray[1].(string); ok {
			push.StreamName = streamName
		}

		// Parse TargetURI
		if targetURI, ok := pushArray[2].(string); ok {
			push.TargetURI = targetURI
		}

		// Parse ActualURI
		if len(pushArray) > 3 {
			if actualURI, ok := pushArray[3].(string); ok {
				push.ActualURI = actualURI
			}
		}

		// Parse Logs (optional)
		if len(pushArray) > 4 {
			if logs, ok := pushArray[4].([]interface{}); ok {
				for _, logRaw := range logs {
					if logStr, ok := logRaw.(string); ok {
						push.Logs = append(push.Logs, logStr)
					}
				}
			}
		}

		// Parse Status (optional)
		if len(pushArray) > 5 {
			if status, ok := pushArray[5].(map[string]interface{}); ok {
				push.Status = status
			}
		}

		pushes = append(pushes, push)
	}

	c.Logger.WithField("push_count", len(pushes)).Debug("Retrieved MistServer push list")
	return pushes, nil
}

// PushStop stops a push by ID
func (c *Client) PushStop(pushID int) error {
	command := map[string]interface{}{
		"push_stop": pushID,
	}

	_, err := c.makeAPIRequest(command)
	if err != nil {
		return fmt.Errorf("push_stop failed: %w", err)
	}

	c.Logger.WithField("push_id", pushID).Info("Stopped MistServer push")
	return nil
}

// GetStreamInfo gets information about a specific stream
func (c *Client) GetStreamInfo(streamName string) (*StreamInfo, error) {
	command := map[string]interface{}{
		"streams": true,
	}

	response, err := c.makeAPIRequest(command)
	if err != nil {
		return nil, fmt.Errorf("streams query failed: %w", err)
	}

	// Parse streams response
	streamsRaw, exists := response["streams"]
	if !exists {
		return nil, fmt.Errorf("stream %s not found", streamName)
	}

	streamsMap, ok := streamsRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected streams format")
	}

	streamRaw, exists := streamsMap[streamName]
	if !exists {
		return nil, fmt.Errorf("stream %s not found", streamName)
	}

	streamMap, ok := streamRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected stream format")
	}

	stream := &StreamInfo{
		Name:     streamName,
		Metadata: streamMap,
	}

	// Parse source
	if source, ok := streamMap["source"].(string); ok {
		stream.Source = source
	}

	// Determine if active (has viewers or is being pushed)
	if viewers, ok := streamMap["curr"]; ok {
		if viewerArray, ok := viewers.([]interface{}); ok && len(viewerArray) > 0 {
			if totalViewers, ok := viewerArray[0].(float64); ok && totalViewers > 0 {
				stream.Active = true
			}
		}
	}

	return stream, nil
}

// FindPushByStream finds a push by stream name
func (c *Client) FindPushByStream(streamName string) (*PushInfo, error) {
	pushes, err := c.PushList()
	if err != nil {
		return nil, err
	}

	for _, push := range pushes {
		if push.StreamName == streamName {
			return &push, nil
		}
	}

	return nil, fmt.Errorf("no push found for stream %s", streamName)
}

// BuildDVRTarget builds the DVR recording target URI
func BuildDVRTarget(storagePath, dvrHash string, config map[string]interface{}) string {
	// Extract config values with defaults
	segmentDuration := 6
	if duration, ok := config["segment_duration"].(int); ok && duration > 0 {
		segmentDuration = duration
	}

	retentionSeconds := 7200 // 2 hours default
	if retention, ok := config["retention_days"].(int); ok && retention > 0 {
		retentionSeconds = retention * 24 * 3600 // Convert days to seconds
	}

	// Build target path with DVR parameters
	// Format: /path/segments/$minute_$segmentCounter.ts?m3u8=../manifest.m3u8&split=6&targetAge=7200&append=1&noendlist=1
	target := fmt.Sprintf("%s/%s/$minute_$segmentCounter.ts?m3u8=../%s.m3u8&split=%d&targetAge=%d&append=1&noendlist=1",
		storagePath,
		dvrHash,
		dvrHash,
		segmentDuration,
		retentionSeconds,
	)

	return target
}

// authenticate handles MistServer TCP API authentication using MD5 challenge-response
func (c *Client) authenticate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("MIST_API_URL not configured")
	}

	// Step 1: Get the challenge
	challengeReq := map[string]interface{}{
		"authorize": map[string]interface{}{
			"username": c.Username,
			"password": "",
		},
	}

	// Use direct call without auth requirement
	resp1, err := c.callAPI(challengeReq)
	if err != nil {
		return fmt.Errorf("failed to send challenge request: %w", err)
	}

	// Check for authorize response
	authInfo, ok := resp1["authorize"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no authorize field in challenge response")
	}

	status, ok := authInfo["status"].(string)
	if !ok {
		return fmt.Errorf("no status in authorize response")
	}

	// If already authenticated (OK status), we're done
	if status == "OK" {
		c.Logger.Debug("Already authenticated with MistServer")
		c.authenticated = true
		return nil
	}

	// If NOACC, no accounts exist
	if status == "NOACC" {
		return fmt.Errorf("no accounts exist on MistServer")
	}

	// If CHALL, proceed with authentication
	if status != "CHALL" {
		return fmt.Errorf("unexpected auth status: %s", status)
	}

	challenge, ok := authInfo["challenge"].(string)
	if !ok {
		return fmt.Errorf("no challenge in response")
	}

	c.Logger.WithFields(logging.Fields{
		"challenge": challenge,
	}).Debug("Got MistServer challenge")

	// Step 2: Calculate password hash using MD5(MD5(password) + challenge)
	passwordHash := c.calculatePasswordHash(c.Password, challenge)

	// Step 3: Send authentication
	authReq := map[string]interface{}{
		"authorize": map[string]interface{}{
			"username": c.Username,
			"password": passwordHash,
		},
	}

	resp2, err := c.callAPI(authReq)
	if err != nil {
		return fmt.Errorf("failed to send auth request: %w", err)
	}

	// Check final auth status
	if finalAuth, ok := resp2["authorize"].(map[string]interface{}); ok {
		if finalStatus, ok := finalAuth["status"].(string); ok && finalStatus == "OK" {
			c.Logger.Debug("Successfully authenticated with MistServer")
			c.authenticated = true
			return nil
		}
	}

	return fmt.Errorf("authentication failed")
}

// calculatePasswordHash calculates MD5(MD5(password) + challenge)
func (c *Client) calculatePasswordHash(password, challenge string) string {
	// First MD5: hash the password
	passwordMD5 := md5.Sum([]byte(password))
	passwordMD5Hex := hex.EncodeToString(passwordMD5[:])

	// Second MD5: hash(passwordMD5 + challenge)
	finalMD5 := md5.Sum([]byte(passwordMD5Hex + challenge))
	return hex.EncodeToString(finalMD5[:])
}

// FetchJSON fetches data from MistServer metrics endpoint (/{secret}.json)
func (c *Client) FetchJSON(endpoint string) (map[string]interface{}, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("MIST_API_URL not configured")
	}

	// Build URL for metrics JSON: /{secret}.json
	base := strings.TrimRight(c.BaseURL, "/")
	var urlStr string
	if endpoint == "" {
		urlStr = fmt.Sprintf("%s/%s.json", base, c.MetricsPassword)
	} else if strings.HasSuffix(endpoint, ".json") {
		urlStr = fmt.Sprintf("%s/%s/%s", base, c.MetricsPassword, endpoint)
	} else {
		urlStr = fmt.Sprintf("%s/%s.json", base, c.MetricsPassword)
	}

	resp, err := c.httpClient.Get(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JSON from %s: %w", urlStr, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, urlStr)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return data, nil
}

// GetActiveStreams fetches active streams using the authenticated API
func (c *Client) GetActiveStreams() (map[string]interface{}, error) {
	command := map[string]interface{}{
		"active_streams": map[string]interface{}{
			"longform": true,
			"fields": []string{
				"clients", "viewers", "inputs", "outputs", "tracks",
				"upbytes", "downbytes", "packsent", "packloss", "packretrans",
				"firstms", "lastms", "health", "pid", "tags", "status",
			},
		},
	}

	response, err := c.makeAPIRequest(command)
	if err != nil {
		return nil, fmt.Errorf("active_streams query failed: %w", err)
	}

	return response, nil
}

// GetClients fetches client metrics using the API
func (c *Client) GetClients() (map[string]interface{}, error) {
	command := map[string]interface{}{
		"clients": map[string]interface{}{
			// Omit fields to receive all available; rely on returned "fields" array for mapping
			"time": -5,
		},
	}

	response, err := c.makeAPIRequest(command)
	if err != nil {
		return nil, fmt.Errorf("clients query failed: %w", err)
	}

	return response, nil
}

// Helper function for min operation
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
