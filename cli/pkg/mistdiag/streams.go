package mistdiag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	fwssh "frameworks/cli/pkg/ssh"
)

const mistAPIBase = "http://localhost:8080"

// ActiveStream represents a stream discovered on a MistServer instance.
type ActiveStream struct {
	Name   string
	Source string
	HLSURL string
}

// DiscoverStreams queries MistServer for active streams via the runner.
func DiscoverStreams(ctx context.Context, runner fwssh.Runner, mode string) ([]ActiveStream, error) {
	apiCmd := fmt.Sprintf(
		`curl -sf '%s/api2?command={"active_streams":true}'`,
		mistAPIBase,
	)

	if mode == "docker" {
		apiCmd = fmt.Sprintf("docker exec mistserver sh -c %s", fwssh.ShellQuote(apiCmd))
	}

	result, err := runner.Run(ctx, apiCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to query MistServer active_streams: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("MistServer API returned exit code %d: %s", result.ExitCode, result.Stderr)
	}

	return parseActiveStreams(result.Stdout)
}

func parseActiveStreams(body string) ([]ActiveStream, error) {
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %w", err)
	}

	streamsRaw, ok := resp["active_streams"]
	if !ok {
		return nil, nil
	}

	var streams []ActiveStream

	switch v := streamsRaw.(type) {
	case map[string]interface{}:
		// longform: map of stream name → info
		for name, info := range v {
			s := ActiveStream{
				Name:   name,
				HLSURL: StreamHLSURL(name),
			}
			if infoMap, ok := info.(map[string]interface{}); ok {
				if src, ok := infoMap["source"].(string); ok {
					s.Source = src
				}
			}
			streams = append(streams, s)
		}
	case []interface{}:
		// shortform: array of stream names
		for _, item := range v {
			if name, ok := item.(string); ok {
				streams = append(streams, ActiveStream{
					Name:   name,
					HLSURL: StreamHLSURL(name),
				})
			}
		}
	}

	return streams, nil
}

// StreamHLSURL returns the local HLS manifest URL for a stream.
func StreamHLSURL(streamName string) string {
	safe := strings.ReplaceAll(streamName, "'", "")
	return fmt.Sprintf("%s/hls/%s/index.m3u8", mistAPIBase, safe)
}
