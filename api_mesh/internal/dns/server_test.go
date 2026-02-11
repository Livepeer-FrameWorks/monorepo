package dns

import (
	"testing"

	"frameworks/pkg/logging"
)

func TestUpdateRecordsStoresFQDNs(t *testing.T) {
	server := NewServer(logging.NewLogger(), 0)

	err := server.UpdateRecords(map[string][]string{
		"edge-1": {"10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, ok := server.records["edge-1.internal."]; !ok {
		t.Fatalf("expected fqdn record to be stored, got %+v", server.records)
	}
}

func TestUpdateRecordsRejectsInvalidIPAndPreservesState(t *testing.T) {
	server := NewServer(logging.NewLogger(), 0)

	err := server.UpdateRecords(map[string][]string{
		"edge-1": {"10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = server.UpdateRecords(map[string][]string{
		"edge-1": {"invalid-ip"},
	})
	if err == nil {
		t.Fatal("expected error for invalid ip")
	}

	if _, ok := server.records["edge-1.internal."]; !ok {
		t.Fatalf("expected previous records to be preserved, got %+v", server.records)
	}
}

func TestUpdateRecordsNormalizesToLowerCase(t *testing.T) {
	server := NewServer(logging.NewLogger(), 0)

	err := server.UpdateRecords(map[string][]string{
		"Edge-1": {"10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, ok := server.records["edge-1.internal."]; !ok {
		t.Fatalf("expected normalized fqdn record to be stored, got %+v", server.records)
	}
	if _, ok := server.records["Edge-1.internal."]; ok {
		t.Fatalf("did not expect mixed-case fqdn record, got %+v", server.records)
	}
}
