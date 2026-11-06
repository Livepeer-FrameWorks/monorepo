package handlers_test

import (
	"bytes"
	"encoding/json"
	"testing"
)

func stackInvoiceFinancialSnapshot(raw json.RawMessage) ([]byte, error) {
	var invoice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &invoice); err != nil {
		return nil, err
	}
	delete(invoice, "metadata")
	if string(bytes.TrimSpace(invoice["status"])) == `"draft"` {
		// These customer fields follow the current buyer until finalization.
		// Every amount, tax line, owner and collection field remains compared.
		for _, field := range []string{"customer_name", "customer_email", "customer_address", "customer_tax_ids"} {
			delete(invoice, field)
		}
	}
	return json.Marshal(invoice)
}

func TestStackUnsupportedDraftAndOperatorInvoiceSnapshots(t *testing.T) {
	draft := map[string]any{
		"id": "in_unsupported", "status": "draft", "billing_reason": "subscription_update",
		"currency": "eur", "subtotal": 5000, "total": 5000, "amount_paid": 0, "amount_due": 5000, "total_excluding_tax": 5000,
		"total_taxes": []any{}, "default_tax_rates": []any{}, "auto_advance": false, "collection_method": "charge_automatically",
		"parent":        map[string]any{"subscription_details": map[string]any{"subscription": "sub_owned"}},
		"lines":         map[string]any{"data": []any{map[string]any{"id": "il_original", "amount": 5000, "currency": "eur", "taxes": []any{}}}},
		"customer_name": "Original Buyer", "customer_email": "before@example.test",
		"customer_address": map[string]string{"country": "AT", "line1": "Old Street"},
		"customer_tax_ids": []any{map[string]string{"type": "eu_vat", "value": "ATU12345678"}},
		"metadata":         map[string]string{},
	}
	clone := func(source map[string]any) map[string]any {
		encoded, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err = json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	snapshot := func(source map[string]any) []byte {
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		result, err := stackInvoiceFinancialSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	operator := clone(draft)
	operator["id"], operator["status"] = "in_operator_paused", "open"
	beforeDraft, beforeOperator := snapshot(draft), snapshot(operator)
	updatedBuyer := map[string]any{
		"customer_name": "Current Buyer", "customer_email": "after@example.test",
		"customer_address": map[string]string{"country": "NL", "line1": "Current Street"}, "customer_tax_ids": []any{},
	}
	// Customer updates affect unsupported drafts too. The already finalized
	// operator invoice keeps its issued buyer and collection preference.
	for key, value := range updatedBuyer {
		draft[key] = value
	}
	draft["metadata"] = map[string]string{"frameworks_tax_terminal_review": "unsupported_lines"}
	if !bytes.Equal(beforeDraft, snapshot(draft)) {
		t.Error("legitimate mutable draft buyer updates failed the money-preservation assertion")
	}
	if !bytes.Equal(beforeOperator, snapshot(operator)) {
		t.Error("untouched finalized operator invoice failed its preservation assertion")
	}
	for field, value := range updatedBuyer {
		t.Run("finalized_"+field, func(t *testing.T) {
			changed := clone(operator)
			changed[field] = value
			if bytes.Equal(beforeOperator, snapshot(changed)) {
				t.Fatal("finalized buyer mutation was hidden")
			}
		})
	}
	for field, value := range map[string]any{
		"status": "open", "currency": "usd", "subtotal": 6000, "total": 6050, "amount_paid": 5000, "amount_due": 0, "total_excluding_tax": 6000,
		"auto_advance": true, "collection_method": "send_invoice", "billing_reason": "subscription_cycle",
		"total_taxes": []any{map[string]any{"amount": 1050}}, "default_tax_rates": []any{"txr_changed"},
		"parent": map[string]any{"subscription_details": map[string]any{"subscription": "sub_other"}},
		"lines":  map[string]any{"data": []any{map[string]any{"id": "il_original", "amount": 6000, "currency": "eur", "taxes": []any{}}}},
	} {
		t.Run("protected_"+field, func(t *testing.T) {
			changed := clone(draft)
			changed[field] = value
			if bytes.Equal(snapshot(draft), snapshot(changed)) {
				t.Fatal("financial, tax, ownership or collection mutation was hidden")
			}
		})
	}
}
