package provisioner

import "testing"

func TestDatabaseNamesFromMetadata(t *testing.T) {
	metadata := map[string]interface{}{
		"databases": []interface{}{
			"  quartermaster ",
			map[string]string{"name": "commodore"},
			map[string]interface{}{"name": "purser"},
			map[string]interface{}{"name": ""},
		},
	}

	names := databaseNamesFromMetadata(metadata)
	if len(names) != 3 {
		t.Fatalf("expected 3 database names, got %d: %#v", len(names), names)
	}
}

func TestDatabaseNamesFromMetadataEmpty(t *testing.T) {
	if names := databaseNamesFromMetadata(nil); names != nil {
		t.Fatalf("expected nil for empty metadata, got %#v", names)
	}
}
