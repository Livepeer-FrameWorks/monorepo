package configschema

import (
	"reflect"
	"testing"
)

func TestInvalidScalarValuesAndSharedEnums(t *testing.T) {
	schema := Schema{Services: []Section{{Service: "test", Variables: []Variable{
		{Key: "SECRET", Type: "integer", Secret: true},
		{Key: "ENABLED", Type: "boolean"}, {Key: "TIMEOUT", Type: "duration"},
		{Key: "LOG_LEVEL", Type: "string"}, {Key: "GRPC_METADATA_POLICY", Type: "string"},
	}}}}
	if got := schema.Invalid("test", map[string]string{
		"SECRET": "do-not-leak", "ENABLED": "yes", "TIMEOUT": "never", "LOG_LEVEL": "verbose", "GRPC_METADATA_POLICY": "maybe",
	}); !reflect.DeepEqual(got, []string{"SECRET", "ENABLED", "TIMEOUT", "LOG_LEVEL", "GRPC_METADATA_POLICY"}) {
		t.Fatalf("invalid keys: %v", got)
	}
	if got := schema.Invalid("test", map[string]string{
		"SECRET": "42", "ENABLED": "true", "TIMEOUT": "1s", "LOG_LEVEL": "Warning", "GRPC_METADATA_POLICY": "Deny",
	}); len(got) != 0 {
		t.Fatalf("valid values rejected: %v", got)
	}
}
