// Package prompts implements MCP prompts for guided agent interactions.
package prompts

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPrompts registers all MCP prompts.
func RegisterPrompts(server *mcp.Server, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) {
	// onboarding - Guide new users through account setup
	server.AddPrompt(&mcp.Prompt{
		Name:        "onboarding",
		Description: "Walk through account setup: billing details, balance top-up, first stream.",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return handleOnboardingPrompt(ctx, clients, checker, logger)
	})

	// create_live_stream - Guide through stream creation
	server.AddPrompt(&mcp.Prompt{
		Name:        "create_live_stream",
		Description: "Step-by-step guide to create a stream and start broadcasting.",
		Arguments: []*mcp.PromptArgument{
			{Name: "stream_name", Description: "Name for the new stream", Required: false},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		name := ""
		if req.Params != nil && req.Params.Arguments != nil {
			if n, ok := req.Params.Arguments["stream_name"]; ok {
				name = n
			}
		}
		return handleCreateStreamPrompt(ctx, name, clients, checker, logger)
	})

	// troubleshoot_stream - Diagnose stream issues
	server.AddPrompt(&mcp.Prompt{
		Name:        "troubleshoot_stream",
		Description: "Diagnose issues with a stream (not working, poor quality, viewers can't connect).",
		Arguments: []*mcp.PromptArgument{
			{Name: "stream_id", Description: "Stream ID to troubleshoot", Required: true},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		streamID := ""
		if req.Params != nil && req.Params.Arguments != nil {
			if id, ok := req.Params.Arguments["stream_id"]; ok {
				streamID = id
			}
		}
		return handleTroubleshootPrompt(ctx, streamID, clients, logger)
	})

	// optimize_costs - Analyze usage and suggest savings
	server.AddPrompt(&mcp.Prompt{
		Name:        "optimize_costs",
		Description: "Analyze current usage patterns and suggest cost optimizations.",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return handleOptimizeCostsPrompt(ctx, clients, logger)
	})

	// capabilities - Explain platform features
	server.AddPrompt(&mcp.Prompt{
		Name:        "capabilities",
		Description: "Explain what FrameWorks can do: live streaming, DVR, clips, analytics.",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return handleCapabilitiesPrompt()
	})

	// video_consultant - Expert video streaming persona
	server.AddPrompt(&mcp.Prompt{
		Name:        "video_consultant",
		Description: "Establishes you as a video streaming expert with knowledge of codecs, protocols, latency optimization, and QoE debugging.",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return handleVideoConsultantPrompt()
	})

	// diagnose_quality_issue - Guided troubleshooting workflow
	server.AddPrompt(&mcp.Prompt{
		Name:        "diagnose_quality_issue",
		Description: "Step-by-step workflow for diagnosing stream quality issues.",
		Arguments: []*mcp.PromptArgument{
			{Name: "stream_id", Description: "Stream ID to diagnose", Required: true},
			{Name: "symptom", Description: "Observed symptom (e.g., buffering, low quality, latency)", Required: false},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		streamID := ""
		symptom := ""
		if req.Params != nil && req.Params.Arguments != nil {
			if id, ok := req.Params.Arguments["stream_id"]; ok {
				streamID = id
			}
			if s, ok := req.Params.Arguments["symptom"]; ok {
				symptom = s
			}
		}
		return handleDiagnoseQualityIssuePrompt(streamID, symptom)
	})

	// api_integration_assistant - Help developers integrate with the API
	server.AddPrompt(&mcp.Prompt{
		Name:        "api_integration_assistant",
		Description: "Guides developers through exploring the API schema, constructing queries, and building integrations.",
		Arguments: []*mcp.PromptArgument{
			{Name: "goal", Description: "What you want to accomplish (e.g., list streams, create clip)", Required: false},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		goal := ""
		if req.Params != nil && req.Params.Arguments != nil {
			if g, ok := req.Params.Arguments["goal"]; ok {
				goal = g
			}
		}
		return handleAPIIntegrationAssistantPrompt(goal)
	})
}

func handleOnboardingPrompt(ctx context.Context, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.GetPromptResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)

	var steps []string
	steps = append(steps, "# FrameWorks Account Setup\n")

	if tenantID == "" {
		steps = append(steps, "## Step 1: Authentication")
		steps = append(steps, "You need to authenticate first. Connect with your Ethereum wallet or use an API token.")
		steps = append(steps, "\n**For wallet auth**: Include X-Wallet-Address, X-Wallet-Signature, and X-Wallet-Message headers.")
		steps = append(steps, "**For API token**: Use a Bearer token in the Authorization header.")
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: strings.Join(steps, "\n")},
			}},
		}, nil
	}

	// Check blockers
	blockers, err := checker.GetBlockers(ctx)
	if err != nil {
		logger.WithError(err).Warn("Failed to get blockers for onboarding")
	}

	if len(blockers) == 0 {
		steps = append(steps, "Your account is fully set up and ready to use.")
		steps = append(steps, "\n## What you can do now:")
		steps = append(steps, "- **Create streams** using the `create_stream` tool")
		steps = append(steps, "- **View analytics** using the `analytics://usage` resource")
		steps = append(steps, "- **Create clips** from live streams using `create_clip`")
		steps = append(steps, "- **Enable DVR** for time-shifted playback with `start_dvr`")
		steps = append(steps, "\n**Note for autonomous agents**: Active streams incur ongoing costs.")
		steps = append(steps, "Monitor `billing://balance` for drain rate and top up before depletion.")
	} else {
		steps = append(steps, "## Setup Required\n")
		steps = append(steps, "Complete these steps to unlock all features:\n")

		for i, blocker := range blockers {
			steps = append(steps, fmt.Sprintf("### Step %d: %s", i+1, blocker.Message))
			steps = append(steps, fmt.Sprintf("**How to fix**: %s", blocker.Resolution))
			if blocker.Tool != "" {
				steps = append(steps, fmt.Sprintf("**Use tool**: `%s`", blocker.Tool))
			}
			steps = append(steps, "")
		}
	}

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(steps, "\n")},
		}},
	}, nil
}

func handleCreateStreamPrompt(ctx context.Context, streamName string, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.GetPromptResult, error) {
	var steps []string
	steps = append(steps, "# Create a Live Stream\n")

	// Check if ready
	blockers, _ := checker.GetBlockers(ctx)
	if len(blockers) > 0 {
		steps = append(steps, "**Note**: Before creating a stream, complete your account setup:")
		for _, b := range blockers {
			steps = append(steps, fmt.Sprintf("- %s (use `%s`)", b.Message, b.Tool))
		}
		steps = append(steps, "")
	}

	steps = append(steps, "## Step 1: Create the Stream")
	if streamName != "" {
		steps = append(steps, fmt.Sprintf("Use the `create_stream` tool with name: \"%s\"", streamName))
	} else {
		steps = append(steps, "Use the `create_stream` tool with:")
		steps = append(steps, "- `name`: Your stream's display name")
		steps = append(steps, "- `record`: Set to true to enable DVR recording")
	}

	steps = append(steps, "\n## Step 2: Configure Your Encoder")
	steps = append(steps, "After creating the stream, you'll receive a stream key. Configure your encoder:")
	steps = append(steps, "- **OBS/Streamlabs**: Settings → Stream → Custom → Enter RTMP URL and stream key")
	steps = append(steps, "- **WHIP**: Use the WHIP endpoint for browser-based streaming")

	steps = append(steps, "\n## Step 3: Start Broadcasting")
	steps = append(steps, "Start your encoder. The stream will be live within seconds.")

	steps = append(steps, "\n## Step 4: Share with Viewers")
	steps = append(steps, "Use `resolve_playback_endpoint` with the playback_id to get viewer URLs.")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(steps, "\n")},
		}},
	}, nil
}

func handleTroubleshootPrompt(ctx context.Context, streamID string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.GetPromptResult, error) {
	var steps []string
	steps = append(steps, "# Stream Troubleshooting\n")

	if streamID == "" {
		steps = append(steps, "Please provide a stream_id to troubleshoot.")
		steps = append(steps, "You can find stream IDs by reading the `streams://list` resource.")
		steps = append(steps, "IDs accept either Relay `id` or the stable `stream_id` UUID.")
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: strings.Join(steps, "\n")},
			}},
		}, nil
	}

	steps = append(steps, fmt.Sprintf("Troubleshooting stream: %s\n", streamID))
	steps = append(steps, "Note: stream IDs accept Relay `id` or the stable `stream_id` UUID.")

	steps = append(steps, "## Diagnostic Steps\n")
	steps = append(steps, "### 1. Check Stream Status")
	steps = append(steps, fmt.Sprintf("Read `streams://%s` to verify the stream exists and is configured correctly.", streamID))

	steps = append(steps, "\n### 2. Check Stream Health")
	steps = append(steps, fmt.Sprintf("Read `streams://%s/health` to check:", streamID))
	steps = append(steps, "- Is the stream receiving data?")
	steps = append(steps, "- What's the current bitrate and FPS?")
	steps = append(steps, "- Are there packet loss issues?")

	steps = append(steps, "\n### 3. Common Issues and Fixes")
	steps = append(steps, "**Stream not starting**:")
	steps = append(steps, "- Verify stream key in encoder matches")
	steps = append(steps, "- Check encoder is sending to correct RTMP URL")
	steps = append(steps, "- Try refreshing stream key with `refresh_stream_key`")

	steps = append(steps, "\n**Poor quality**:")
	steps = append(steps, "- Check encoder bitrate (recommended: 4500-6000 kbps for 1080p)")
	steps = append(steps, "- Ensure stable upload connection")
	steps = append(steps, "- Verify encoder preset (use 'veryfast' for CPU encoding)")

	steps = append(steps, "\n**Viewers can't connect**:")
	steps = append(steps, "- Use `resolve_playback_endpoint` to get fresh playback URLs")
	steps = append(steps, "- Check if stream is actually live (not just created)")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(steps, "\n")},
		}},
	}, nil
}

func handleOptimizeCostsPrompt(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.GetPromptResult, error) {
	var steps []string
	steps = append(steps, "# Cost Optimization Guide\n")

	steps = append(steps, "## Current Usage")
	steps = append(steps, "Read `analytics://usage` to see your current month's usage.")
	steps = append(steps, "Read `billing://balance` to check your current balance and drain rate.\n")

	steps = append(steps, "## Cost-Saving Strategies\n")

	steps = append(steps, "### 1. Optimize Streaming Settings")
	steps = append(steps, "- Use adaptive bitrate (ABR) to reduce bandwidth for low-quality viewers")
	steps = append(steps, "- Consider 720p instead of 1080p for most content (40% bandwidth savings)")

	steps = append(steps, "\n### 2. Manage Storage")
	steps = append(steps, "- Set expiration on clips to auto-delete after a period")
	steps = append(steps, "- Disable DVR recording for streams that don't need it")
	steps = append(steps, "- Delete unused VOD assets")

	steps = append(steps, "\n### 3. Geographic Optimization")
	steps = append(steps, "- Read `analytics://geographic` to see where your viewers are")
	steps = append(steps, "- Consider edge node placement near viewer concentrations")

	steps = append(steps, "\n### 4. Monitor Usage")
	steps = append(steps, "- Set up low balance alerts")
	steps = append(steps, "- Review daily usage patterns")
	steps = append(steps, "- Identify unused streams")

	steps = append(steps, "\n## Billing Information")
	steps = append(steps, "Read `billing://pricing` to see current rates for:")
	steps = append(steps, "- Bandwidth (per GB delivered)")
	steps = append(steps, "- Storage (per GB/month)")
	steps = append(steps, "- Processing (per minute transcoded)")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(steps, "\n")},
		}},
	}, nil
}

func handleCapabilitiesPrompt() (*mcp.GetPromptResult, error) {
	var parts []string
	parts = append(parts, "# FrameWorks Platform Capabilities\n")
	parts = append(parts, "FrameWorks is a multi-tenant live streaming platform with the following features:\n")

	parts = append(parts, "## Live Streaming")
	parts = append(parts, "- **RTMP/WHIP Ingest**: Stream from OBS, StreamLabs, or browser-based WHIP clients")
	parts = append(parts, "- **Multi-protocol Output**: HLS, DASH, WebRTC playback")
	parts = append(parts, "- **Low Latency**: Sub-second latency with WebRTC, 2-4s with LL-HLS")
	parts = append(parts, "- **Adaptive Bitrate**: Automatic quality switching based on viewer connection\n")

	parts = append(parts, "## Recording & VOD")
	parts = append(parts, "- **DVR Recording**: Time-shift live streams for catch-up viewing")
	parts = append(parts, "- **Clips**: Create shareable clips from live or recorded streams")
	parts = append(parts, "- **VOD Upload**: Upload pre-recorded content for on-demand playback\n")

	parts = append(parts, "## Analytics")
	parts = append(parts, "- **Real-time Metrics**: Viewer counts, quality metrics, error rates")
	parts = append(parts, "- **Geographic Distribution**: See where your viewers are located")
	parts = append(parts, "- **Usage Tracking**: Monitor bandwidth, storage, and processing usage\n")

	parts = append(parts, "## API & Automation")
	parts = append(parts, "- **GraphQL API**: Full platform control via GraphQL")
	parts = append(parts, "- **MCP Integration**: AI agent access via Model Context Protocol")
	parts = append(parts, "- **Webhooks**: Real-time event notifications\n")

	parts = append(parts, "## Billing")
	parts = append(parts, "- **Prepaid Model**: Top up balance with crypto (ETH, USDC, LPT)")
	parts = append(parts, "- **Usage-based Pricing**: Pay only for what you use")
	parts = append(parts, "- **Transparent Pricing**: Clear rates for bandwidth, storage, compute\n")

	parts = append(parts, "## Cost Model for Long-Running Operations")
	parts = append(parts, "Media operations have **ongoing costs** that continue asynchronously:")
	parts = append(parts, "- **Active streams**: Cost accrues while live (ingest minutes, viewer hours)")
	parts = append(parts, "- **Storage**: DVR recordings, clips, and VOD consume storage continuously")
	parts = append(parts, "- **Processing**: Transcoding happens on-demand during ingest\n")
	parts = append(parts, "For long-running jobs, monitor your balance:")
	parts = append(parts, "- Read `billing://balance` to see your drain rate (cents per hour)")
	parts = append(parts, "- If `estimated_hours_left` is low, top up proactively")
	parts = append(parts, "- Streams will fail if balance depletes mid-session\n")

	parts = append(parts, "## Getting Started")
	parts = append(parts, "1. Read account://status to check your account state")
	parts = append(parts, "2. Complete any required setup (billing details, balance)")
	parts = append(parts, "3. Create your first stream with the create_stream tool")
	parts = append(parts, "4. Use resolve_playback_endpoint to get viewer URLs\n")

	parts = append(parts, "Need help? Use the onboarding or troubleshoot_stream prompts for guided assistance.")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(parts, "\n")},
		}},
	}, nil
}

func handleVideoConsultantPrompt() (*mcp.GetPromptResult, error) {
	var parts []string
	parts = append(parts, "# Video Streaming Consultant\n")
	parts = append(parts, "You are an expert video streaming consultant with deep knowledge of:")
	parts = append(parts, "- **Codecs**: H.264, HEVC, VP9, AV1 encoding settings and trade-offs")
	parts = append(parts, "- **Protocols**: RTMP, HLS, DASH, WebRTC, WHIP/WHEP")
	parts = append(parts, "- **Latency optimization**: GOP intervals, segment duration, buffer tuning")
	parts = append(parts, "- **Quality of Experience**: Rebuffering analysis, adaptive bitrate, viewer metrics")
	parts = append(parts, "- **CDN architecture**: Edge node selection, geographic routing, load balancing\n")

	parts = append(parts, "## Available Tools\n")
	parts = append(parts, "Use these diagnostic tools to analyze stream issues:")
	parts = append(parts, "- `diagnose_rebuffering(stream_id)` - Analyze rebuffer events and patterns")
	parts = append(parts, "- `diagnose_buffer_health(stream_id)` - Check buffer state and dry events")
	parts = append(parts, "- `diagnose_packet_loss(stream_id)` - Analyze packet loss with protocol-aware guidance")
	parts = append(parts, "- `diagnose_routing(stream_id)` - Review CDN routing decisions")
	parts = append(parts, "- `get_stream_health_summary(stream_id)` - Get aggregated health metrics")
	parts = append(parts, "- `get_anomaly_report(stream_id)` - Detect statistical anomalies")
	parts = append(parts, "- `search_support_history(query)` - Find relevant past conversations\n")

	parts = append(parts, "## Knowledge Sources\n")
	parts = append(parts, "Read `knowledge://sources` to get authoritative documentation:")
	parts = append(parts, "- FrameWorks platform docs (ingest, playback, API)")
	parts = append(parts, "- MistServer configuration and protocols")
	parts = append(parts, "- FFmpeg encoding guides")
	parts = append(parts, "- OBS setup and troubleshooting\n")

	parts = append(parts, "## Approach\n")
	parts = append(parts, "When helping with stream issues:")
	parts = append(parts, "1. Gather symptoms and context from the user")
	parts = append(parts, "2. Use diagnostic tools to collect metrics")
	parts = append(parts, "3. Cross-reference with knowledge sources")
	parts = append(parts, "4. Check support history for similar past issues")
	parts = append(parts, "5. Provide actionable recommendations with clear explanations\n")

	parts = append(parts, "Be specific and technical. Avoid generic advice - dig into the actual data.")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(parts, "\n")},
		}},
	}, nil
}

func handleDiagnoseQualityIssuePrompt(streamID, symptom string) (*mcp.GetPromptResult, error) {
	var parts []string
	parts = append(parts, "# Stream Quality Diagnosis Workflow\n")

	if streamID == "" {
		parts = append(parts, "**Missing stream_id** - Please provide the stream ID to diagnose.")
		parts = append(parts, "You can find stream IDs by reading the `streams://list` resource.")
		parts = append(parts, "IDs accept Relay `id` or the stable `stream_id` UUID.")
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: strings.Join(parts, "\n")},
			}},
		}, nil
	}

	parts = append(parts, fmt.Sprintf("Diagnosing stream: **%s**\n", streamID))
	parts = append(parts, "Note: stream IDs accept Relay `id` or the stable `stream_id` UUID.\n")

	if symptom != "" {
		parts = append(parts, fmt.Sprintf("Reported symptom: **%s**\n", symptom))
	}

	parts = append(parts, "## Step 1: Collect Baseline Metrics")
	parts = append(parts, fmt.Sprintf("Run `get_stream_health_summary` for stream `%s` to understand current state.\n", streamID))

	parts = append(parts, "## Step 2: Symptom-Specific Analysis")

	if symptom != "" {
		symptomLower := strings.ToLower(symptom)
		if strings.Contains(symptomLower, "buffer") || strings.Contains(symptomLower, "stutter") || strings.Contains(symptomLower, "pause") {
			parts = append(parts, "**Buffering/Stuttering detected**:")
			parts = append(parts, fmt.Sprintf("- Run `diagnose_rebuffering(stream_id: \"%s\")`", streamID))
			parts = append(parts, fmt.Sprintf("- Run `diagnose_buffer_health(stream_id: \"%s\")`", streamID))
			parts = append(parts, "- Check encoder bitrate vs viewer connection quality")
		} else if strings.Contains(symptomLower, "loss") || strings.Contains(symptomLower, "packet") || strings.Contains(symptomLower, "drop") {
			parts = append(parts, "**Packet loss detected**:")
			parts = append(parts, fmt.Sprintf("- Run `diagnose_packet_loss(stream_id: \"%s\")`", streamID))
			parts = append(parts, "- Verify protocol and network path for loss sensitivity")
		} else if strings.Contains(symptomLower, "quality") || strings.Contains(symptomLower, "pixelat") || strings.Contains(symptomLower, "blur") {
			parts = append(parts, "**Quality issues detected**:")
			parts = append(parts, fmt.Sprintf("- Run `get_stream_health_summary(stream_id: \"%s\")`", streamID))
			parts = append(parts, "- Check quality_tier distribution in metrics")
			parts = append(parts, "- Review encoder preset and bitrate settings")
		} else if strings.Contains(symptomLower, "latency") || strings.Contains(symptomLower, "delay") || strings.Contains(symptomLower, "lag") {
			parts = append(parts, "**Latency issues detected**:")
			parts = append(parts, "- Check GOP interval (recommended: 2 seconds for low-latency)")
			parts = append(parts, "- Review segment duration settings")
			parts = append(parts, fmt.Sprintf("- Run `diagnose_routing(stream_id: \"%s\")` to check CDN path", streamID))
		} else {
			parts = append(parts, "**General diagnosis**:")
			parts = append(parts, fmt.Sprintf("- Run `diagnose_rebuffering(stream_id: \"%s\")`", streamID))
			parts = append(parts, fmt.Sprintf("- Run `get_anomaly_report(stream_id: \"%s\")`", streamID))
		}
	} else {
		parts = append(parts, "Run all diagnostic tools to build a complete picture:")
		parts = append(parts, fmt.Sprintf("- `diagnose_rebuffering(stream_id: \"%s\")`", streamID))
		parts = append(parts, fmt.Sprintf("- `diagnose_buffer_health(stream_id: \"%s\")`", streamID))
		parts = append(parts, fmt.Sprintf("- `diagnose_packet_loss(stream_id: \"%s\")`", streamID))
		parts = append(parts, fmt.Sprintf("- `diagnose_routing(stream_id: \"%s\")`", streamID))
		parts = append(parts, fmt.Sprintf("- `get_anomaly_report(stream_id: \"%s\")`", streamID))
	}

	parts = append(parts, "\n## Step 3: Historical Context")
	parts = append(parts, "Search for similar past issues:")
	parts = append(parts, "- `search_support_history(query: \"buffering\")` or relevant keywords")
	parts = append(parts, fmt.Sprintf("- Check `support://conversations` for past discussions about stream %s", streamID))

	parts = append(parts, "\n## Step 4: Recommendations")
	parts = append(parts, "Based on the collected data, provide:")
	parts = append(parts, "1. Root cause analysis")
	parts = append(parts, "2. Specific encoder/configuration changes")
	parts = append(parts, "3. Relevant documentation links from `knowledge://sources`")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(parts, "\n")},
		}},
	}, nil
}

func handleAPIIntegrationAssistantPrompt(goal string) (*mcp.GetPromptResult, error) {
	var parts []string
	parts = append(parts, "# FrameWorks API Integration Assistant\n")
	parts = append(parts, "You are helping a developer integrate with the FrameWorks GraphQL API.\n")

	if goal != "" {
		parts = append(parts, fmt.Sprintf("**Developer's goal**: %s\n", goal))
	}

	parts = append(parts, "## Available Tools\n")
	parts = append(parts, "- `introspect_schema(focus, depth)` - Explore the API schema")
	parts = append(parts, "  - focus: `query`, `mutation`, `subscription`, or a type name")
	parts = append(parts, "  - depth: 1-4 (1=field names, 2=+args, 3=+nested types, 4=full details)")
	parts = append(parts, "- `generate_query(field_path, operation_type)` - Get a ready-to-use query")
	parts = append(parts, "  - field_path supports nested paths like `analytics.usage.streaming.viewerHoursHourlyConnection`")
	parts = append(parts, "  - Uses real templates from the codebase when available")
	parts = append(parts, "  - If no template matches, use introspect_schema to build a custom query\n")

	parts = append(parts, "## Available Resources\n")
	parts = append(parts, "- `schema://catalog` - Merged schema + templates + curated examples (requires auth)\n")

	parts = append(parts, "## Workflow\n")
	parts = append(parts, "### Step 1: Discover Available Operations")
	parts = append(parts, "Start with `introspect_schema(focus: \"query\", depth: 1)` to see what's available.")
	parts = append(parts, "Or read `schema://catalog` for curated examples by category.\n")

	parts = append(parts, "### Step 2: Explore Specific Types")
	parts = append(parts, "Use `introspect_schema(focus: \"TypeName\", depth: 2)` to understand:")
	parts = append(parts, "- Input types (what arguments to pass)")
	parts = append(parts, "- Return types (what data you'll get back)\n")

	parts = append(parts, "### Step 3: Generate a Query")
	parts = append(parts, "Use `generate_query(field_path: \"streamsConnection\")` to get:")
	parts = append(parts, "- A complete, valid GraphQL query")
	parts = append(parts, "- Default variables with sensible placeholders")
	parts = append(parts, "- Hints about pagination and common patterns\n")

	parts = append(parts, "### Step 4: Pagination Pattern (Relay Connections)")
	parts = append(parts, "All lists use Relay cursor pagination:")
	parts = append(parts, "```graphql")
	parts = append(parts, "query GetStreams($page: ConnectionInput!) {")
	parts = append(parts, "  streamsConnection(page: $page) {")
	parts = append(parts, "    pageInfo {")
	parts = append(parts, "      hasNextPage")
	parts = append(parts, "      endCursor")
	parts = append(parts, "    }")
	parts = append(parts, "    edges {")
	parts = append(parts, "      node { id name status }")
	parts = append(parts, "    }")
	parts = append(parts, "  }")
	parts = append(parts, "}")
	parts = append(parts, "```")
	parts = append(parts, "Variables: `{ \"page\": { \"first\": 50, \"after\": null } }`")
	parts = append(parts, "For next page: `{ \"page\": { \"first\": 50, \"after\": \"<endCursor>\" } }`\n")

	parts = append(parts, "### Step 5: Authentication")
	parts = append(parts, "Include one of these headers:")
	parts = append(parts, "- `Authorization: Bearer <jwt_token>` - User session")
	parts = append(parts, "- `Authorization: Bearer <api_token>` - Programmatic access")
	parts = append(parts, "- Wallet headers (X-Wallet-Address, X-Wallet-Signature, X-Wallet-Message)")
	parts = append(parts, "**Note**: Schema tools (`introspect_schema`, `schema://catalog`) require JWT or API token auth.\n")

	parts = append(parts, "## Common Patterns\n")
	parts = append(parts, "**Getting a single resource**: Use the singular query (e.g., `stream(id: $id)`)")
	parts = append(parts, "**Listing resources**: Use the connection query (e.g., `streamsConnection(page: $page)`)")
	parts = append(parts, "**Time-based analytics**: Pass `timeRange: { start: \"...\", end: \"...\" }`")
	parts = append(parts, "**Stream-scoped data**: Pass `streamId` (this is the Relay global ID, not the UUID)\n")

	parts = append(parts, "## Tips\n")
	parts = append(parts, "- Global IDs look like: `Stream:abc123...` - use `stream.id` not `stream.streamId`")
	parts = append(parts, "- Subscriptions require WebSocket connection to `/graphql/ws`")
	parts = append(parts, "- Analytics queries return aggregated data - use appropriate time ranges")

	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: strings.Join(parts, "\n")},
		}},
	}, nil
}
