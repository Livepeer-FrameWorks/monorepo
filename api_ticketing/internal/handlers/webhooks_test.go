package handlers

import (
	"testing"
	"time"
)

func TestGetConversationID_RootLevel(t *testing.T) {
	p := &ChatwootWebhookPayload{ID: 42}
	if got := p.GetConversationID(); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestGetConversationID_Nested(t *testing.T) {
	p := &ChatwootWebhookPayload{
		ID:           99,
		Conversation: &ChatwootConversation{ID: 42},
	}
	if got := p.GetConversationID(); got != 42 {
		t.Fatalf("got %d, want 42 (nested takes priority)", got)
	}
}

func TestGetConversationID_NestedZero(t *testing.T) {
	p := &ChatwootWebhookPayload{
		ID:           99,
		Conversation: &ChatwootConversation{ID: 0},
	}
	if got := p.GetConversationID(); got != 99 {
		t.Fatalf("got %d, want 99 (fallback to root when nested is 0)", got)
	}
}

func TestGetCustomAttributes_Root(t *testing.T) {
	attrs := map[string]any{"tenant_id": "t1"}
	p := &ChatwootWebhookPayload{CustomAttributes: attrs}
	got := p.GetCustomAttributes()
	if got["tenant_id"] != "t1" {
		t.Fatalf("got %v", got)
	}
}

func TestGetCustomAttributes_Nested(t *testing.T) {
	rootAttrs := map[string]any{"root_key": "root"}
	nestedAttrs := map[string]any{"tenant_id": "t2"}
	p := &ChatwootWebhookPayload{
		CustomAttributes: rootAttrs,
		Conversation:     &ChatwootConversation{CustomAttributes: nestedAttrs},
	}
	got := p.GetCustomAttributes()
	if got["tenant_id"] != "t2" {
		t.Fatalf("expected nested attrs, got %v", got)
	}
}

func TestGetCustomAttributes_Nil(t *testing.T) {
	p := &ChatwootWebhookPayload{}
	if got := p.GetCustomAttributes(); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestGetAccountID_Root(t *testing.T) {
	p := &ChatwootWebhookPayload{AccountID: 5}
	if got := p.GetAccountID(); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestGetAccountID_Nested(t *testing.T) {
	p := &ChatwootWebhookPayload{
		Conversation: &ChatwootConversation{AccountID: 7},
	}
	if got := p.GetAccountID(); got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}

func TestGetAccountID_Zero(t *testing.T) {
	p := &ChatwootWebhookPayload{}
	if got := p.GetAccountID(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestGetInboxID_Root(t *testing.T) {
	p := &ChatwootWebhookPayload{InboxID: 3}
	if got := p.GetInboxID(); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestGetInboxID_Nested(t *testing.T) {
	p := &ChatwootWebhookPayload{
		Conversation: &ChatwootConversation{InboxID: 9},
	}
	if got := p.GetInboxID(); got != 9 {
		t.Fatalf("got %d, want 9", got)
	}
}

func TestGetInboxID_Zero(t *testing.T) {
	p := &ChatwootWebhookPayload{}
	if got := p.GetInboxID(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestFormatEnrichmentNote_FullData(t *testing.T) {
	note := formatEnrichmentNote(
		"tenant-123",
		"Acme Corp",
		time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		"billing@acme.com",
		"Pro",
		"active",
		"https://app.example.com/dashboard",
	)

	expects := []string{
		"**Tenant:** Acme Corp",
		"**Email:** billing@acme.com",
		"**Plan:** Pro (active)",
		"**Member since:** Jun 2024",
		"**Page:** https://app.example.com/dashboard",
	}
	for _, want := range expects {
		if !contains(note, want) {
			t.Errorf("note missing %q\ngot:\n%s", want, note)
		}
	}
}

func TestFormatEnrichmentNote_MinimalData(t *testing.T) {
	note := formatEnrichmentNote("tenant-456", "", time.Time{}, "", "", "", "")
	if !contains(note, "**Tenant ID:** tenant-456") {
		t.Errorf("expected tenant ID fallback, got:\n%s", note)
	}
	if contains(note, "**Email:**") {
		t.Error("should not include email when empty")
	}
	if contains(note, "**Plan:**") {
		t.Error("should not include plan when empty")
	}
	if contains(note, "**Member since:**") {
		t.Error("should not include member since for zero time")
	}
	if contains(note, "**Page:**") {
		t.Error("should not include page when empty")
	}
}

func TestFormatEnrichmentNote_PlanWithoutStatus(t *testing.T) {
	note := formatEnrichmentNote("t", "", time.Time{}, "", "Enterprise", "", "")
	if !contains(note, "**Plan:** Enterprise\n") {
		t.Errorf("expected plan without status parenthetical, got:\n%s", note)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
