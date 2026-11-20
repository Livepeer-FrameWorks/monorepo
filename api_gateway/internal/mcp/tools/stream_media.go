package tools

import (
	"context"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ListStreamMediaInput struct {
	StreamID string  `json:"stream_id" jsonschema:"Stream UUID or GraphQL ID"`
	First    *int    `json:"first,omitempty" jsonschema:"Page size, up to 100"`
	After    *string `json:"after,omitempty" jsonschema:"Opaque end cursor from the previous page"`
}

type GetDVRRecordingInput struct {
	ID              string  `json:"id" jsonschema:"Recording UUID or DVR hash"`
	IncludeChapters bool    `json:"include_chapters,omitempty" jsonschema:"Include a page of saved chapters"`
	First           *int    `json:"first,omitempty" jsonschema:"Chapter page size, up to 100"`
	After           *string `json:"after,omitempty" jsonschema:"Opaque end cursor from the previous chapter page"`
}

func registerStreamMediaTools(server *mcp.Server, resolver *resolvers.Resolver) {
	addTool(server, &mcp.Tool{Name: "list_stream_recordings", Description: "List a stream's saved recordings with cursor pagination."}, listStreamMediaHandler(resolver, "dvr"))
	addTool(server, &mcp.Tool{Name: "list_stream_clips", Description: "List a stream's saved clips with cursor pagination."}, listStreamMediaHandler(resolver, "clip"))
	addTool(server, &mcp.Tool{Name: "get_dvr_recording", Description: "Read a saved recording's catalog state and optionally a page of its saved chapters."}, func(ctx context.Context, _ *mcp.CallToolRequest, args GetDVRRecordingInput) (*mcp.CallToolResult, any, error) {
		recording, err := resolver.DoGetDVRRecording(ctx, args.ID)
		if err != nil {
			return nil, nil, err
		}
		result := map[string]any{"recording": recording}
		if args.IncludeChapters {
			chapters, chapterErr := resolver.DoSavedDVRChapters(ctx, recording, &model.ConnectionInput{First: args.First, After: args.After}, nil, nil)
			if chapterErr != nil {
				return nil, nil, chapterErr
			}
			result["chapters"] = chapters
		}
		return toolSuccess(result)
	})
}

func listStreamMediaHandler(resolver *resolvers.Resolver, kind string) func(context.Context, *mcp.CallToolRequest, ListStreamMediaInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args ListStreamMediaInput) (*mcp.CallToolResult, any, error) {
		id, err := decodeStreamID(args.StreamID)
		if err != nil {
			return toolError(err.Error())
		}
		stream, err := resolver.DoGetStream(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		connection, err := resolver.DoStreamArtifacts(ctx, stream, kind, &model.ConnectionInput{First: args.First, After: args.After})
		if err != nil {
			return nil, nil, err
		}
		return toolSuccess(connection)
	}
}
