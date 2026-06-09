package chatwoot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCreateContact_OKStatusSucceeds pins that a 200 OK (not just 201 Created)
// is accepted by CreateContact. The accept set is {200, 201}; a mutant that
// collapses one clause would reject 200.
func TestCreateContact_OKStatusSucceeds(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newJSONResponse(http.StatusOK, `{
			"payload":{"contact":{"id":7,"identifier":"tenant-7","name":"Seven"}}
		}`), nil
	}))

	contact, err := client.CreateContact(context.Background(), "tenant-7", "Seven", "")
	if err != nil {
		t.Fatalf("CreateContact with 200 OK should succeed, got %v", err)
	}
	if contact.ID != 7 {
		t.Errorf("contact ID = %d, want 7", contact.ID)
	}
}

// TestCreateContact_OtherStatusErrors pins that a status outside {200,201}
// (here 422) is an error. This is the failing branch the negation mutant masks.
func TestCreateContact_OtherStatusErrors(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newJSONResponse(http.StatusUnprocessableEntity, `{"message":"bad"}`), nil
	}))

	if _, err := client.CreateContact(context.Background(), "tenant-x", "X", "x@example.com"); err == nil {
		t.Fatal("CreateContact with 422 should error")
	}
}

// TestSearchConversations_ZeroPageOmitsParam pins the page boundary: page=0
// must not add a "page" query param (only page > 0 paginates).
func TestSearchConversations_ZeroPageOmitsParam(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Has("page") {
			t.Errorf("page=0 must not set page param, got %q", req.URL.Query().Get("page"))
		}
		return newJSONResponse(http.StatusOK, `{"data":{"payload":[]}}`), nil
	}))

	if _, _, err := client.SearchConversations(context.Background(), "q", 0); err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}
}

// TestSearchConversations_PositivePageSetsParam pins the other side of the
// boundary: page=1 must set page=1.
func TestSearchConversations_PositivePageSetsParam(t *testing.T) {
	client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("page"); got != "1" {
			t.Errorf("page param = %q, want 1", got)
		}
		return newJSONResponse(http.StatusOK, `{"data":{"payload":[]}}`), nil
	}))

	if _, _, err := client.SearchConversations(context.Background(), "q", 1); err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}
}

// TestSendMessage_CreatedStatusSucceedsOtherErrors pins the {200,201} accept
// set for SendMessage: 201 Created succeeds and a 5xx errors.
func TestSendMessage_CreatedStatusSucceedsOtherErrors(t *testing.T) {
	t.Run("201 Created succeeds", func(t *testing.T) {
		client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusCreated, `{"id":3,"content":"hi"}`), nil
		}))
		msg, err := client.SendMessage(context.Background(), 5, "hi", false)
		if err != nil {
			t.Fatalf("SendMessage 201 should succeed, got %v", err)
		}
		if msg.ID != 3 {
			t.Errorf("msg ID = %d, want 3", msg.ID)
		}
	})
	t.Run("500 errors", func(t *testing.T) {
		client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusInternalServerError, `{}`), nil
		}))
		if _, err := client.SendMessage(context.Background(), 5, "hi", false); err == nil {
			t.Fatal("SendMessage 500 should error")
		}
	})
}

// TestGetConversation_NotFoundReturnsNilNil pins the 404 short-circuit:
// a missing conversation returns (nil, nil), and a 200 returns the decoded
// conversation. A negated status check would conflate the two.
func TestGetConversation_NotFoundReturnsNilNil(t *testing.T) {
	t.Run("404 returns nil,nil", func(t *testing.T) {
		client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusNotFound, `{}`), nil
		}))
		conv, err := client.GetConversation(context.Background(), 1)
		if err != nil {
			t.Fatalf("404 should not error, got %v", err)
		}
		if conv != nil {
			t.Errorf("404 should return nil conversation, got %+v", conv)
		}
	})
	t.Run("200 returns conversation", func(t *testing.T) {
		client := newClientWithTransport(testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusOK, `{"id":88,"status":"open"}`), nil
		}))
		conv, err := client.GetConversation(context.Background(), 88)
		if err != nil {
			t.Fatalf("GetConversation: %v", err)
		}
		if conv == nil || conv.ID != 88 {
			t.Fatalf("want conversation ID 88, got %+v", conv)
		}
	})
}

// TestDoRequest_BodyTransmittedOnPostAndOmittedOnGet pins that a non-nil body
// is marshaled and sent, and a nil body (GET) sends no request body.
func TestDoRequest_BodyTransmittedOnPostAndOmittedOnGet(t *testing.T) {
	t.Run("POST sends marshaled body", func(t *testing.T) {
		client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Body == nil {
				t.Fatal("expected a request body on POST")
			}
			b, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(b), `"content":"hello"`) {
				t.Errorf("body = %q, want it to contain content:hello", string(b))
			}
			return newJSONResponse(http.StatusOK, `{"id":1}`), nil
		}))
		if _, err := client.SendMessage(context.Background(), 5, "hello", false); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	})

	t.Run("GET sends no body", func(t *testing.T) {
		client := newClientWithTransport(testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Body != nil {
				b, _ := io.ReadAll(req.Body)
				if len(b) != 0 {
					t.Errorf("GET must send no body, got %q", string(b))
				}
			}
			return newJSONResponse(http.StatusOK, `{}`), nil
		}))
		if _, err := client.GetConversation(context.Background(), 9); err != nil {
			t.Fatalf("GetConversation: %v", err)
		}
	})
}
