package listmonk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL  string
	username string
	password string
	client   *http.Client
}

func NewClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  baseURL,
		username: username,
		password: password,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
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

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("listmonk returned status: %d", resp.StatusCode)
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

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("listmonk returned status: %d", resp.StatusCode)
	}

	return nil
}

// Unsubscribe removes a subscriber from a specific list (or blocklists if listID is 0?)
// Listmonk API usually handles unsubscribe via PUT /api/subscribers/{id} or public unsubscription.
// For API usage, we might just want to remove from a specific list.
// For now, let's stick to Subscribe (Upsert).
