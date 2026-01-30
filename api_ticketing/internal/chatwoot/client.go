package chatwoot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client wraps the Chatwoot API
type Client struct {
	baseURL    string
	apiToken   string
	accountID  int
	inboxID    int
	httpClient *http.Client
}

// Config holds client configuration
type Config struct {
	BaseURL   string
	APIToken  string
	AccountID int
	InboxID   int
}

// NewClient creates a new Chatwoot API client
func NewClient(cfg Config) *Client {
	return &Client{
		baseURL:   cfg.BaseURL,
		apiToken:  cfg.APIToken,
		accountID: cfg.AccountID,
		inboxID:   cfg.InboxID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Contact represents a Chatwoot contact
type Contact struct {
	ID               int64             `json:"id"`
	Identifier       string            `json:"identifier,omitempty"` // External identifier (tenant_id)
	Name             string            `json:"name"`
	Email            string            `json:"email,omitempty"`
	CustomAttributes map[string]string `json:"custom_attributes,omitempty"`
}

// Conversation represents a Chatwoot conversation
type Conversation struct {
	ID               int64             `json:"id"`
	AccountID        int64             `json:"account_id"`
	InboxID          int64             `json:"inbox_id"`
	Status           string            `json:"status"` // open, resolved, pending
	CreatedAt        int64             `json:"created_at"`
	LastActivityAt   int64             `json:"last_activity_at,omitempty"`
	UnreadCount      int               `json:"unread_count"`
	CustomAttributes map[string]string `json:"custom_attributes,omitempty"`
	Meta             *ConversationMeta `json:"meta,omitempty"`
	Messages         []Message         `json:"messages,omitempty"`
}

// ConversationMeta contains metadata about a conversation
type ConversationMeta struct {
	Sender *Contact `json:"sender,omitempty"`
}

// Message represents a Chatwoot message
type Message struct {
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	Content        string `json:"content"`
	ContentType    string `json:"content_type"` // text, input_select, cards, form
	MessageType    int    `json:"message_type"` // 0=incoming, 1=outgoing, 2=activity
	Private        bool   `json:"private"`
	CreatedAt      int64  `json:"created_at"`
	Sender         *struct {
		ID   int64  `json:"id"`
		Type string `json:"type"` // user, contact
		Name string `json:"name"`
	} `json:"sender,omitempty"`
}

// MessageType constants matching Chatwoot's enum
const (
	MessageTypeIncoming = 0 // From contact (customer)
	MessageTypeOutgoing = 1 // From agent
	MessageTypeActivity = 2 // System activity message
)

// FindOrCreateContact finds a contact by source_id or creates one.
// If a contact exists with the same email but different source_id, updates it.
func (c *Client) FindOrCreateContact(ctx context.Context, sourceID, name, email string) (*Contact, error) {
	// First try to find by source_id
	contact, err := c.GetContactBySourceID(ctx, sourceID)
	if err == nil && contact != nil {
		return contact, nil
	}

	// Try to create new contact
	contact, err = c.CreateContact(ctx, sourceID, name, email)
	if err == nil {
		return contact, nil
	}

	// If creation failed due to duplicate email, find by email and update source_id
	if email != "" {
		contact, err = c.GetContactByEmail(ctx, email)
		if err == nil && contact != nil {
			// Update the contact's source_id
			return c.UpdateContactSourceID(ctx, contact.ID, sourceID)
		}
	}

	return nil, fmt.Errorf("failed to find or create contact: %w", err)
}

// GetContactBySourceID finds a contact by their source identifier (tenant_id)
func (c *Client) GetContactBySourceID(ctx context.Context, sourceID string) (*Contact, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/search?q=%s", c.baseURL, c.accountID, sourceID)

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("search contacts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search contacts: status %d", resp.StatusCode)
	}

	var result struct {
		Payload []Contact `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Find exact match by identifier
	for _, contact := range result.Payload {
		if contact.Identifier == sourceID {
			return &contact, nil
		}
	}

	return nil, nil // Not found
}

// GetContactByEmail finds a contact by email address
func (c *Client) GetContactByEmail(ctx context.Context, email string) (*Contact, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/search?q=%s", c.baseURL, c.accountID, email)

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("search contacts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search contacts: status %d", resp.StatusCode)
	}

	var result struct {
		Payload []Contact `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Find exact match by email
	for _, contact := range result.Payload {
		if contact.Email == email {
			return &contact, nil
		}
	}

	return nil, nil // Not found
}

// UpdateContactSourceID updates a contact's source_id field
func (c *Client) UpdateContactSourceID(ctx context.Context, contactID int64, sourceID string) (*Contact, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/%d", c.baseURL, c.accountID, contactID)

	body := map[string]interface{}{
		"identifier": sourceID,
	}

	resp, err := c.doRequest(ctx, "PUT", url, body)
	if err != nil {
		return nil, fmt.Errorf("update contact: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("update contact: status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Payload Contact `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result.Payload, nil
}

// CreateContact creates a new contact in Chatwoot
func (c *Client) CreateContact(ctx context.Context, sourceID, name, email string) (*Contact, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts", c.baseURL, c.accountID)

	body := map[string]interface{}{
		"inbox_id":   c.inboxID,
		"identifier": sourceID,
		"name":       name,
		"email":      email,
	}

	resp, err := c.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("create contact: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create contact: status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Payload struct {
			Contact Contact `json:"contact"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result.Payload.Contact, nil
}

// ListConversations returns conversations for a contact
func (c *Client) ListConversations(ctx context.Context, contactID int64, page, perPage int) ([]Conversation, int, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/%d/conversations", c.baseURL, c.accountID, contactID)

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("list conversations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("list conversations: status %d", resp.StatusCode)
	}

	var result struct {
		Payload []Conversation `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return result.Payload, len(result.Payload), nil
}

// SearchConversations searches conversations across the account using a query string.
func (c *Client) SearchConversations(ctx context.Context, query string, page int) ([]Conversation, int, error) {
	endpoint, err := url.Parse(fmt.Sprintf("%s/api/v1/accounts/%d/conversations", c.baseURL, c.accountID))
	if err != nil {
		return nil, 0, fmt.Errorf("build search url: %w", err)
	}

	params := endpoint.Query()
	params.Set("q", query)
	params.Set("status", "all")
	if page > 0 {
		params.Set("page", fmt.Sprintf("%d", page))
	}
	endpoint.RawQuery = params.Encode()

	resp, err := c.doRequest(ctx, "GET", endpoint.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("search conversations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("search conversations: status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Payload []Conversation `json:"payload"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}

	return result.Data.Payload, len(result.Data.Payload), nil
}

// GetConversation returns a single conversation by ID
func (c *Client) GetConversation(ctx context.Context, conversationID int64) (*Conversation, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d", c.baseURL, c.accountID, conversationID)

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get conversation: status %d", resp.StatusCode)
	}

	var conv Conversation
	if err := json.NewDecoder(resp.Body).Decode(&conv); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &conv, nil
}

// CreateConversation creates a new conversation for a contact
func (c *Client) CreateConversation(ctx context.Context, contactID int64, subject string, customAttrs map[string]string) (*Conversation, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations", c.baseURL, c.accountID)

	// NOTE: Chatwoot has no native "subject" field for conversations.
	// Store subject in custom_attributes (business data), not additional_attributes (browser info).
	if subject != "" {
		if customAttrs == nil {
			customAttrs = make(map[string]string)
		}
		customAttrs["subject"] = subject
	}

	body := map[string]interface{}{
		"inbox_id":          c.inboxID,
		"contact_id":        contactID,
		"custom_attributes": customAttrs,
	}

	resp, err := c.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("create conversation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create conversation: status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var conv Conversation
	if err := json.NewDecoder(resp.Body).Decode(&conv); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &conv, nil
}

// ListMessages returns messages in a conversation
func (c *Client) ListMessages(ctx context.Context, conversationID int64) ([]Message, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages", c.baseURL, c.accountID, conversationID)

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list messages: status %d", resp.StatusCode)
	}

	// Chatwoot returns {"payload": [...messages...], "meta": {...}}
	var result struct {
		Payload []Message `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Payload, nil
}

// SendMessage sends a message in a conversation
func (c *Client) SendMessage(ctx context.Context, conversationID int64, content string, private bool) (*Message, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages", c.baseURL, c.accountID, conversationID)

	body := map[string]interface{}{
		"content":      content,
		"message_type": "incoming", // From customer to agent
		"private":      private,
	}

	resp, err := c.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("send message: status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var msg Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &msg, nil
}

// CreateNote adds a private note to a conversation (for enrichment)
func (c *Client) CreateNote(ctx context.Context, conversationID int64, content string) (*Message, error) {
	return c.SendMessage(ctx, conversationID, content, true)
}

// doRequest executes an HTTP request with auth headers
func (c *Client) doRequest(ctx context.Context, method, url string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", c.apiToken)

	return c.httpClient.Do(req)
}

// Ping checks Chatwoot API connectivity for the configured account.
func (c *Client) Ping(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d", c.baseURL, c.accountID)
	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chatwoot ping: status %d", resp.StatusCode)
	}
	return nil
}
