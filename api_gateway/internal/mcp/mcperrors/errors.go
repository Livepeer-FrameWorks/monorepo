package mcperrors

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const ResourceMetadataURL = "https://api.frameworks.network/.well-known/oauth-protected-resource"

func AuthRequired() error {
	data, _ := json.Marshal(map[string]any{
		"resource_metadata": ResourceMetadataURL,
	})
	return &jsonrpc.Error{
		Code:    -32001,
		Message: "not authenticated",
		Data:    data,
	}
}
