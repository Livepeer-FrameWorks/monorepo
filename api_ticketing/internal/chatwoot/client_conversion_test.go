package chatwoot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newClientWithTransport(rt http.RoundTripper) *Client {
	c := NewClient(Config{
		BaseURL:   "https://chatwoot.test",
		APIToken:  "token",
		AccountID: 99,
		InboxID:   7,
	})
	c.httpClient = &http.Client{Transport: rt}
	return c
}

func TestGetContactBySourceID_InlineDecodeAndExactMatch(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method: got %s, want GET", req.Method)
		}
		if req.URL.Path != "/api/v1/accounts/99/contacts/search" {
			t.Fatalf("path: got %q", req.URL.Path)
		}
		if req.URL.Query().Get("q") != "tenant-123" {
			t.Fatalf("query: got %q", req.URL.Query().Get("q"))
		}
		return newJSONResponse(http.StatusOK, `{
			"payload":[
				{"id":1,"identifier":"tenant-other","name":"Other"},
				{"id":2,"identifier":"tenant-123","name":"Exact","email":"exact@example.com"}
			]
		}`), nil
	}))

	contact, err := client.GetContactBySourceID(context.Background(), "tenant-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contact == nil {
		t.Fatal("expected contact")
	}
	if contact.ID != 2 || contact.Identifier != "tenant-123" || contact.Email != "exact@example.com" {
		t.Fatalf("unexpected contact mapping: %+v", contact)
	}
}

func TestGetContactBySourceID_DecodeError(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newJSONResponse(http.StatusOK, `{"payload":`), nil
	}))

	_, err := client.GetContactBySourceID(context.Background(), "tenant-123")
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestGetContactByEmail_InlineDecodeAndExactMatch(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("q") != "user@example.com" {
			t.Fatalf("query: got %q", req.URL.Query().Get("q"))
		}
		return newJSONResponse(http.StatusOK, `{
			"payload":[
				{"id":10,"email":"other@example.com","name":"Other"},
				{"id":11,"email":"user@example.com","name":"Exact"}
			]
		}`), nil
	}))

	contact, err := client.GetContactByEmail(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contact == nil || contact.ID != 11 || contact.Email != "user@example.com" {
		t.Fatalf("unexpected contact mapping: %+v", contact)
	}
}

func TestUpdateContactSourceID_InlinePayloadMapping(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("method: got %s, want PUT", req.Method)
		}
		return newJSONResponse(http.StatusOK, `{
			"payload":{"id":44,"identifier":"tenant-updated","name":"Updated","email":"up@example.com"}
		}`), nil
	}))

	contact, err := client.UpdateContactSourceID(context.Background(), 44, "tenant-updated")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contact.ID != 44 || contact.Identifier != "tenant-updated" || contact.Email != "up@example.com" {
		t.Fatalf("unexpected contact mapping: %+v", contact)
	}
}

func TestCreateContact_InlineNestedPayloadMapping(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method: got %s, want POST", req.Method)
		}
		return newJSONResponse(http.StatusCreated, `{
			"payload":{
				"contact":{"id":55,"identifier":"tenant-55","name":"Tenant 55","email":"tenant55@example.com"}
			}
		}`), nil
	}))

	contact, err := client.CreateContact(context.Background(), "tenant-55", "Tenant 55", "tenant55@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contact.ID != 55 || contact.Identifier != "tenant-55" || contact.Email != "tenant55@example.com" {
		t.Fatalf("unexpected contact mapping: %+v", contact)
	}
}

func TestListConversations_InlinePayloadMapping(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method: got %s, want GET", req.Method)
		}
		return newJSONResponse(http.StatusOK, `{
			"payload":[
				{"id":101,"account_id":99,"inbox_id":7,"status":"open","created_at":1700000000,"unread_count":3}
			]
		}`), nil
	}))

	conversations, total, err := client.ListConversations(context.Background(), 123, 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(conversations) != 1 {
		t.Fatalf("unexpected conversations count: total=%d len=%d", total, len(conversations))
	}
	if conversations[0].ID != 101 || conversations[0].Status != "open" || conversations[0].UnreadCount != 3 {
		t.Fatalf("unexpected conversation mapping: %+v", conversations[0])
	}
}

func TestSearchConversations_InlineDataPayloadMapping(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if q.Get("q") != "buffering" {
			t.Fatalf("expected query 'buffering', got %q", q.Get("q"))
		}
		if q.Get("status") != "all" {
			t.Fatalf("expected status 'all', got %q", q.Get("status"))
		}
		if q.Get("page") != "2" {
			t.Fatalf("expected page '2', got %q", q.Get("page"))
		}
		return newJSONResponse(http.StatusOK, `{
			"data":{
				"payload":[
					{"id":202,"account_id":99,"inbox_id":7,"status":"resolved","created_at":1700000100,"unread_count":0}
				]
			}
		}`), nil
	}))

	conversations, total, err := client.SearchConversations(context.Background(), "buffering", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(conversations) != 1 {
		t.Fatalf("unexpected conversations count: total=%d len=%d", total, len(conversations))
	}
	if conversations[0].ID != 202 || conversations[0].Status != "resolved" {
		t.Fatalf("unexpected conversation mapping: %+v", conversations[0])
	}
}

func TestListMessages_InlinePayloadDecodeError(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newJSONResponse(http.StatusOK, `{"payload":`), nil
	}))

	_, err := client.ListMessages(context.Background(), 42)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}
