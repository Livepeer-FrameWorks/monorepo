package mcp

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	mcpToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "bridge",
		Subsystem: "mcp",
		Name:      "tool_calls_total",
		Help:      "Total MCP tool calls by tool, risk class, and outcome.",
	}, []string{"tool", "risk", "outcome"})

	mcpToolDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "bridge",
		Subsystem: "mcp",
		Name:      "tool_duration_seconds",
		Help:      "MCP tool-call duration in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"tool", "risk"})

	mcpToolResultBytes = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "bridge",
		Subsystem: "mcp",
		Name:      "tool_result_bytes",
		Help:      "Encoded MCP tool-result size in bytes.",
		Buckets:   prometheus.ExponentialBuckets(256, 2, 10),
	}, []string{"tool"})

	mcpToolDenialsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "bridge",
		Subsystem: "mcp",
		Name:      "tool_denials_total",
		Help:      "Total MCP tool calls denied before or after handler execution.",
	}, []string{"tool", "reason"})

	mcpResourceDenialsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "bridge",
		Subsystem: "mcp",
		Name:      "resource_denials_total",
		Help:      "Total MCP resource reads denied by scope class and reason.",
	}, []string{"scope", "reason"})
)
