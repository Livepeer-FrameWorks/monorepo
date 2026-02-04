package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/gin-gonic/gin"
)

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatBitsPerSec(bps uint64) string {
	const unit = 1000
	if bps < unit {
		return fmt.Sprintf("%d bps", bps)
	}
	div, exp := int64(unit), 0
	for n := bps / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cbps", float64(bps)/float64(div), "KMG"[exp])
}

// formatBytesPerSec converts bytes/sec to human-readable bits/sec.
// MistServer and NodeLifecycleUpdate report bandwidth in bytes/sec,
// but network bandwidth is conventionally displayed in bits/sec.
func formatBytesPerSec(bytesPerSec uint64) string {
	return formatBitsPerSec(bytesPerSec * 8)
}

// safeInt extracts int from interface, returns 0 if invalid
func safeInt(v interface{}) int {
	val, _ := toInt(v)
	return val
}

// safeUint64 extracts uint64 from interface, returns 0 if invalid
func safeUint64(v interface{}) uint64 {
	val, _ := toInt64(v)
	return uint64(val)
}

func findStreamSourceNodeID(stream *state.StreamState, instances map[string]map[string]state.StreamInstanceState) string {
	if stream != nil && stream.NodeID != "" {
		return stream.NodeID
	}
	if stream == nil {
		return ""
	}
	nodeInstances := instances[stream.InternalName]
	if len(nodeInstances) == 0 {
		return ""
	}
	var bestID string
	var bestAt time.Time
	for nodeID, inst := range nodeInstances {
		if inst.Inputs > 0 && !inst.Replicated && inst.Status != "offline" {
			if bestID == "" || inst.LastUpdate.After(bestAt) {
				bestID = nodeID
				bestAt = inst.LastUpdate
			}
		}
	}
	return bestID
}

// HandleRootPage serves a debug webpage showing Foghorn's internal state
func HandleRootPage(c *gin.Context) {
	// Generate timestamp for page generation
	generatedAt := time.Now()

	// Get load balancer configuration
	weights := lb.GetWeights()

	// Get all nodes
	nodes := lb.GetAllNodes()

	// Get stream states from the state manager
	streamStates := state.DefaultManager().GetAllStreamStates()

	// Get stream instances (per-node stream data)
	streamInstances := state.DefaultManager().GetAllStreamInstances()

	// Sort nodes by host for consistent display
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Host < nodes[j].Host
	})

	// Sort streams by internal name
	sort.Slice(streamStates, func(i, j int) bool {
		return streamStates[i].InternalName < streamStates[j].InternalName
	})

	// Calculate cluster statistics
	activeNodes := 0
	totalViewers := uint64(0)
	totalStreams := 0
	totalArtifacts := 0
	totalPendingRedirects := 0

	for _, node := range nodes {
		if node.IsActive {
			activeNodes++
		}
		totalViewers += getTotalViewers(node)
		totalStreams += len(node.Streams)
		totalArtifacts += len(node.Artifacts)
		totalPendingRedirects += node.PendingRedirects
	}

	// Get virtual viewer stats
	virtualViewerStats := state.DefaultManager().GetVirtualViewerStats()

	// HTML template with Slab design system - COMPREHENSIVE DEBUG VERSION
	htmlTemplate := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Foghorn - Debug Dashboard</title>
    <meta http-equiv="refresh" content="10">
    <style>
        :root {
            --bg-app: #0d1117;
            --bg-slab: #161b22;
            --bg-row: #1c2128;
            --border-seam: #30363d;
            --text-primary: #f0f6fc;
            --text-secondary: #8b949e;
            --accent: #58a6ff;
            --success: #3fb950;
            --danger: #da3633;
            --warning: #d29922;
            --purple: #a371f7;
        }
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
            background: var(--bg-app);
            color: var(--text-primary);
            line-height: 1.5;
            padding: 20px;
            font-size: 13px;
        }
        .container { max-width: 1600px; margin: 0 auto; }
        h1 { font-size: 20px; margin-bottom: 20px; display: flex; align-items: center; gap: 10px; }
        h1 .emoji { font-size: 24px; }
        .grid-2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1px; background: var(--border-seam); margin-bottom: 20px; }
        .slab { background: var(--bg-slab); border: 1px solid var(--border-seam); margin-bottom: 20px; }
        .slab-header { padding: 12px 16px; border-bottom: 1px solid var(--border-seam); font-weight: 600; display: flex; justify-content: space-between; align-items: center; }
        .slab-body { padding: 16px; }
        .slab-row { padding: 12px 16px; border-bottom: 1px solid var(--border-seam); }
        .slab-row:last-child { border-bottom: none; }
        .metric-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 16px; }
        .metric-item .label { font-size: 11px; color: var(--text-secondary); text-transform: uppercase; margin-bottom: 4px; }
        .metric-item .value { font-size: 16px; font-weight: 600; }
        .metric-item .value.mono { font-family: monospace; font-size: 13px; }
        .tag { display: inline-block; padding: 2px 8px; background: rgba(88,166,255,0.15); color: var(--accent); border-radius: 3px; font-size: 11px; margin: 2px; }
        .tag.success { background: rgba(63,185,80,0.15); color: var(--success); }
        .tag.warning { background: rgba(210,153,34,0.15); color: var(--warning); }
        .tag.danger { background: rgba(218,54,51,0.15); color: var(--danger); }
        .tag.purple { background: rgba(163,113,247,0.15); color: var(--purple); }
        .status-badge { padding: 4px 10px; border-radius: 3px; font-size: 11px; font-weight: 600; }
        .status-active { background: rgba(63,185,80,0.2); color: var(--success); }
        .status-inactive { background: rgba(218,54,51,0.2); color: var(--danger); }
        table { width: 100%; border-collapse: collapse; font-size: 12px; }
        th, td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--border-seam); }
        th { background: rgba(0,0,0,0.3); color: var(--text-secondary); font-weight: 600; font-size: 11px; text-transform: uppercase; }
        tr:hover { background: rgba(88,166,255,0.05); }
        .mono { font-family: monospace; }
        .accent { color: var(--accent); }
        .success { color: var(--success); }
        .warning { color: var(--warning); }
        .danger { color: var(--danger); }
        .secondary { color: var(--text-secondary); }
        .timestamp { text-align: center; padding: 20px; color: var(--text-secondary); font-size: 11px; }
        .section-title { font-size: 14px; font-weight: 600; color: var(--text-secondary); margin: 24px 0 12px 0; text-transform: uppercase; letter-spacing: 1px; }
        .collapsible { cursor: pointer; }
        .collapsible:after { content: ' ▼'; font-size: 10px; }
        .node-card { background: var(--bg-slab); border: 1px solid var(--border-seam); margin-bottom: 16px; }
        .node-header { padding: 12px 16px; border-bottom: 1px solid var(--border-seam); display: flex; justify-content: space-between; align-items: center; }
        .node-content { padding: 0; }
        .detail-row { display: grid; grid-template-columns: 140px 1fr; padding: 8px 16px; border-bottom: 1px solid var(--border-seam); font-size: 12px; }
        .detail-row:last-child { border-bottom: none; }
        .detail-row .label { color: var(--text-secondary); }
        .detail-row .value { font-family: monospace; word-break: break-all; }
        pre { background: var(--bg-row); padding: 12px; border-radius: 4px; overflow-x: auto; font-size: 11px; }
        .artifact-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 12px; padding: 12px; }
        .artifact-card { background: var(--bg-row); border: 1px solid var(--border-seam); padding: 12px; border-radius: 4px; }
        .artifact-card .hash { font-family: monospace; font-size: 11px; color: var(--accent); margin-bottom: 8px; word-break: break-all; }
        .artifact-card .meta { font-size: 11px; color: var(--text-secondary); }
        .artifact-card .meta .mono { font-family: monospace; }
    </style>
</head>
<body>
    <div class="container">
        <h1><span class="emoji">🌫️</span> Foghorn Debug Dashboard</h1>

        <!-- Cluster Overview -->
        <div class="grid-2">
            <div class="slab" style="margin: 0;">
                <div class="slab-header">Cluster Overview</div>
                <div class="slab-body">
                    <div class="metric-grid">
                        <div class="metric-item">
                            <div class="label">Total Nodes</div>
                            <div class="value">{{.TotalNodes}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Active Nodes</div>
                            <div class="value success">{{.ActiveNodes}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Total Streams</div>
                            <div class="value">{{.TotalStreams}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Total Viewers</div>
                            <div class="value accent">{{.TotalViewers}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Live Artifacts</div>
                            <div class="value purple">{{.TotalArtifacts}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">DB Artifacts</div>
                            <div class="value purple">{{.TotalDBArtifacts}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Processing Jobs</div>
                            <div class="value warning">{{.TotalProcessingJobs}}</div>
                        </div>
                    </div>
                </div>
            </div>

            <div class="slab" style="margin: 0;">
                <div class="slab-header">Load Balancer Weights</div>
                <div class="slab-body">
                    <div class="metric-grid">
                        <div class="metric-item">
                            <div class="label">CPU</div>
                            <div class="value">{{.CPUWeight}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">RAM</div>
                            <div class="value">{{.RAMWeight}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Bandwidth</div>
                            <div class="value">{{.BWWeight}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Geo</div>
                            <div class="value">{{.GeoWeight}}</div>
                        </div>
                        <div class="metric-item">
                            <div class="label">Stream Bonus</div>
                            <div class="value">{{.StreamBonus}}</div>
                        </div>
                    </div>
                </div>
            </div>
        </div>

	        <!-- Virtual Viewer Stats -->
	        <div class="slab">
	            <div class="slab-header">🔮 Virtual Viewer Tracking</div>
	            <div class="slab-body">
	                <div class="metric-grid">
                    <div class="metric-item">
                        <div class="label">Total Pending</div>
                        <div class="value {{if gt .VirtualViewerStats.TotalPending 0}}warning{{end}}">{{.VirtualViewerStats.TotalPending}}</div>
                    </div>
                    <div class="metric-item">
                        <div class="label">Total Active</div>
                        <div class="value accent">{{.VirtualViewerStats.TotalActive}}</div>
                    </div>
                    <div class="metric-item">
                        <div class="label">Total Abandoned</div>
                        <div class="value {{if gt .VirtualViewerStats.TotalAbandoned 0}}danger{{end}}">{{.VirtualViewerStats.TotalAbandoned}}</div>
                    </div>
                    <div class="metric-item">
                        <div class="label">Total Tracked</div>
                        <div class="value">{{.VirtualViewerStats.TotalViewers}}</div>
                    </div>
                    <div class="metric-item">
                        <div class="label">Est Pending BW</div>
                        <div class="value">{{.VirtualViewerStats.EstPendingBandwidth}}</div>
                    </div>
	                </div>
	            </div>
	        </div>

	        <!-- Stream Context Cache (tenant/user enrichment) -->
	        <div class="slab">
	            <div class="slab-header">
	                🧭 Stream Context Cache
	                <span class="secondary" style="font-weight: 400;">(tenant/user enrichment)</span>
	                <span class="secondary" style="font-weight: 400;">
	                    <a href="/debug/cache/stream-context" style="color: var(--accent); text-decoration: none;">JSON</a>
	                </span>
	            </div>
	            <div class="slab-body">
	                {{if .StreamContextCache.Enabled}}
	                    <div class="metric-grid">
	                        <div class="metric-item">
	                            <div class="label">Entries</div>
	                            <div class="value">{{.StreamContextCache.Size}}</div>
	                        </div>
	                        <div class="metric-item">
	                            <div class="label">Hits</div>
	                            <div class="value accent">{{.StreamContextCache.Hits}}</div>
	                        </div>
	                        <div class="metric-item">
	                            <div class="label">Misses</div>
	                            <div class="value">{{.StreamContextCache.Misses}}</div>
	                        </div>
	                        <div class="metric-item">
	                            <div class="label">Resolve Errors</div>
	                            <div class="value {{if gt .StreamContextCache.ResolveErrors 0}}danger{{end}}">{{.StreamContextCache.ResolveErrors}}</div>
	                        </div>
	                        <div class="metric-item">
	                            <div class="label">Missing/Zero Tenant</div>
	                            <div class="value {{if gt .StreamContextCache.MissingTenant 0}}danger{{end}}">{{.StreamContextCache.MissingTenant}}</div>
	                        </div>
	                        <div class="metric-item">
	                            <div class="label">Last Resolve</div>
	                            <div class="value mono">{{.StreamContextCache.LastResolveAgo}}</div>
	                        </div>
	                    </div>
	                    {{if .StreamContextCache.LastError}}
	                        <div style="margin-top: 12px;" class="secondary">
	                            <span class="danger">Last error:</span>
	                            <span class="mono">{{.StreamContextCache.LastError}}</span>
	                        </div>
	                    {{end}}

	                    {{if gt (len .StreamContextCache.Entries) 0}}
	                        <div style="margin-top: 16px;">
	                            <table>
	                                <thead>
	                                    <tr>
	                                        <th>Key</th>
	                                        <th>Tenant</th>
	                                        <th>User</th>
	                                        <th>Source</th>
	                                        <th>Updated</th>
	                                    </tr>
	                                </thead>
	                                <tbody>
	                                    {{range .StreamContextCache.Entries}}
	                                    <tr>
	                                        <td class="mono accent">{{.Key}}</td>
	                                        <td class="mono {{if .TenantIsMissing}}danger{{end}}">{{.TenantID}}</td>
	                                        <td class="mono">{{.UserID}}</td>
	                                        <td class="mono secondary">{{.Source}}</td>
	                                        <td class="secondary">{{.UpdatedAgo}}</td>
	                                    </tr>
	                                    {{end}}
	                                </tbody>
	                            </table>
	                        </div>
	                    {{end}}
	                {{else}}
	                    <div class="secondary">Not configured</div>
	                {{end}}
	            </div>
	        </div>

	        <!-- Nodes Section -->
	        <div class="section-title">Nodes ({{.TotalNodes}})</div>
	        {{range .Nodes}}
	        <div class="node-card">
            <div class="node-header">
                <div style="display: flex; align-items: center; gap: 12px;">
                    <span class="status-badge {{if .IsActive}}status-active{{else}}status-inactive{{end}}">
                        {{if .IsActive}}ACTIVE{{else}}INACTIVE{{end}}
                    </span>
                    <span class="mono accent">{{.NodeID}}</span>
                    {{if .LocationName}}<span class="tag">📍 {{.LocationName}}</span>{{end}}
                </div>
                <span class="secondary" style="font-size: 11px;">Updated {{.LastUpdateAgo}}</span>
            </div>
            <div class="node-content">
                <!-- Basic Info -->
                <div class="detail-row"><span class="label">Host URL</span><span class="value">{{.Host}}</span></div>
                <div class="detail-row"><span class="label">Geo Coordinates</span><span class="value">{{printf "%.4f" .GeoLatitude}}, {{printf "%.4f" .GeoLongitude}}</span></div>

                <!-- Resources -->
                <div class="detail-row">
                    <span class="label">CPU</span>
                    <span class="value">{{printf "%.1f" .CPUPercent}}% <span class="secondary">({{.CPUTenths}} tenths)</span></span>
                </div>
                <div class="detail-row">
                    <span class="label">RAM</span>
                    <span class="value">{{.RAMPercent}}% <span class="secondary">({{.RAMCurrentStr}} / {{.RAMMaxStr}})</span></span>
                </div>
                <div class="detail-row">
                    <span class="label">Disk</span>
                    <span class="value">{{.DiskUsedPercent}}% <span class="secondary">({{.DiskUsedBytesStr}} / {{.DiskTotalBytesStr}})</span></span>
                </div>
                <div class="detail-row">
                    <span class="label">Storage Capacity</span>
                    <span class="value">{{.StorageUsedStr}} / {{.StorageCapacityStr}}</span>
                </div>

                <!-- Bandwidth -->
                <div class="detail-row">
                    <span class="label">Bandwidth Up</span>
                    <span class="value">{{.UpSpeedStr}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">Bandwidth Down</span>
                    <span class="value">{{.DownSpeedStr}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">BW Limit</span>
                    <span class="value">{{.BWLimitStr}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">BW Available</span>
                    <span class="value {{if gt .AvailBandwidth 0}}success{{else}}danger{{end}}">{{.AvailBandwidthStr}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">Add BW Penalty</span>
                    <span class="value">{{.AddBandwidthStr}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">Pending Redirects</span>
                    <span class="value {{if gt .PendingRedirects 0}}warning{{end}}">{{.PendingRedirects}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">Est BW/User</span>
                    <span class="value">{{.EstBandwidthStr}}</span>
                </div>

                <!-- Capabilities -->
                <div class="detail-row">
                    <span class="label">Capabilities</span>
                    <span class="value">
                        {{range .Capabilities}}<span class="tag">{{.}}</span>{{end}}
                        {{if not .Capabilities}}<span class="secondary">None</span>{{end}}
                    </span>
                </div>
                <div class="detail-row">
                    <span class="label">Roles</span>
                    <span class="value">
                        {{range .Roles}}<span class="tag purple">{{.}}</span>{{end}}
                        {{if not .Roles}}<span class="secondary">None</span>{{end}}
                    </span>
                </div>

                <!-- Ports -->
                <div class="detail-row">
                    <span class="label">Ports</span>
                    <span class="value">HTTP: {{.Port}} | DTSC: {{.DTSCPort}}</span>
                </div>

                <!-- Storage -->
                <div class="detail-row">
                    <span class="label">Storage Local</span>
                    <span class="value">{{if .StorageLocal}}{{.StorageLocal}}{{else}}<span class="secondary">Not configured</span>{{end}}</span>
                </div>
                <div class="detail-row">
                    <span class="label">Storage Bucket</span>
                    <span class="value">{{if .StorageBucket}}s3://{{.StorageBucket}}/{{.StoragePrefix}}{{else}}<span class="secondary">Not configured</span>{{end}}</span>
                </div>

                <!-- GPU -->
                {{if .GPUCount}}
                <div class="detail-row">
                    <span class="label">GPU</span>
                    <span class="value">{{.GPUCount}}x {{.GPUVendor}} ({{.GPUMemMB}}MB) CC: {{.GPUCC}}</span>
                </div>
                {{end}}

                <!-- Transcodes -->
                <div class="detail-row">
                    <span class="label">Transcodes</span>
                    <span class="value">{{.CurrentTranscodes}} / {{.MaxTranscodes}}</span>
                </div>

                <!-- Config Streams -->
                {{if .ConfigStreams}}
                <div class="detail-row">
                    <span class="label">Config Streams</span>
                    <span class="value">{{range .ConfigStreams}}<span class="tag">{{.}}</span>{{end}}</span>
                </div>
                {{end}}

                <!-- Tags -->
                {{if .Tags}}
                <div class="detail-row">
                    <span class="label">Tags</span>
                    <span class="value">{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</span>
                </div>
                {{end}}

                <!-- Active Streams on Node -->
                {{if .Streams}}
                <div style="padding: 12px 16px; background: rgba(0,0,0,0.2); border-top: 1px solid var(--border-seam);">
                    <div style="font-size: 11px; font-weight: 600; color: var(--text-secondary); margin-bottom: 8px;">ACTIVE STREAMS ({{len .Streams}})</div>
                    <table>
                        <tr>
                            <th>Stream Name</th>
                            <th>Viewers</th>
                            <th>Inputs</th>
                            <th>Bandwidth</th>
                            <th>Bytes Up</th>
                            <th>Bytes Down</th>
                            <th>Replicated</th>
                        </tr>
                        {{range .Streams}}
                        <tr>
                            <td class="mono accent">{{.Name}}</td>
                            <td>{{.Total}}</td>
                            <td>{{.Inputs}}</td>
                            <td>{{.BandwidthStr}}</td>
                            <td>{{.BytesUpStr}}</td>
                            <td>{{.BytesDownStr}}</td>
                            <td>{{if .Replicated}}<span class="tag success">Yes</span>{{else}}<span class="tag">No</span>{{end}}</td>
                        </tr>
                        {{end}}
                    </table>
                </div>
                {{end}}

                <!-- Artifacts on Node -->
                {{if .Artifacts}}
                <div style="padding: 12px 16px; background: rgba(0,0,0,0.15); border-top: 1px solid var(--border-seam);">
                    <div style="font-size: 11px; font-weight: 600; color: var(--text-secondary); margin-bottom: 8px;">ARTIFACTS ({{len .Artifacts}})</div>
                    <div class="artifact-grid">
                        {{range .Artifacts}}
                        <div class="artifact-card">
                            <div class="hash">{{.ClipHash}}</div>
                            <div class="meta">
                                <div>Stream: <span class="accent">{{.StreamName}}</span></div>
                                <div>Format: <span class="tag">{{.Format}}</span></div>
                                <div>Size: {{.SizeStr}}</div>
                                <div>Access: <span class="mono">{{.AccessCount}}</span></div>
                                <div>Last: <span class="mono">{{.LastAccessed}}</span></div>
                                <div>Path: <span style="word-break: break-all;">{{.FilePath}}</span></div>
                            </div>
                        </div>
                        {{end}}
                    </div>
                </div>
                {{end}}
            </div>
        </div>
        {{end}}

        <!-- Stream States Section -->
        <div class="section-title">Stream States ({{len .StreamStates}})</div>
        <div class="slab">
            <div class="slab-body" style="padding: 0; overflow-x: auto;">
                <table>
                    <thead>
                        <tr>
                            <th>Internal Name</th>
                            <th>Node</th>
                            <th>Tenant</th>
                            <th>Status</th>
                            <th>Buffer</th>
                            <th>Viewers</th>
                            <th>Connections</th>
                            <th>Inputs</th>
                            <th>Bytes Up</th>
                            <th>Bytes Down</th>
                            <th>Started</th>
                            <th>Updated</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range .StreamStates}}
                        <tr>
                            <td class="mono accent">{{.InternalName}}</td>
                            <td class="mono">{{.NodeID}}</td>
                            <td class="mono secondary">{{if .TenantID}}{{.TenantID}}{{else}}-{{end}}</td>
                            <td>
                                {{if eq .Status "live"}}<span class="tag success">{{.Status}}</span>
                                {{else if eq .Status "offline"}}<span class="tag danger">{{.Status}}</span>
                                {{else}}<span class="tag">{{.Status}}</span>{{end}}
                            </td>
                            <td>
                                {{if eq .BufferState "FULL"}}<span class="tag success">{{.BufferState}}</span>
                                {{else if .BufferState}}<span class="tag warning">{{.BufferState}}</span>
                                {{else}}<span class="secondary">-</span>{{end}}
                            </td>
                            <td>{{.Viewers}}</td>
                            <td>{{.TotalConnections}}</td>
                            <td>{{.Inputs}}</td>
                            <td>{{.BytesUpStr}}</td>
                            <td>{{.BytesDownStr}}</td>
                            <td class="secondary">{{if .StartedAt}}{{.StartedAt}}{{else}}-{{end}}</td>
                            <td class="secondary">{{.LastUpdateAgo}}</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>

        <!-- Stream Instances Section (Per-Node) -->
        {{if .StreamInstances}}
        <div class="section-title">Stream Instances - Per Node ({{len .StreamInstances}})</div>
        <div class="slab">
            <div class="slab-body" style="padding: 0; overflow-x: auto;">
                <table>
                    <thead>
                        <tr>
                            <th>Stream</th>
                            <th>Node</th>
                            <th>Status</th>
                            <th>Buffer</th>
                            <th>Viewers</th>
                            <th>Connections</th>
                            <th>Inputs</th>
                            <th>Bytes Up</th>
                            <th>Bytes Down</th>
                            <th>Replicated</th>
                            <th>Updated</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range .StreamInstances}}
                        <tr>
                            <td class="mono accent">{{.InternalName}}</td>
                            <td class="mono">{{.NodeID}}</td>
                            <td>
                                {{if eq .Status "live"}}<span class="tag success">{{.Status}}</span>
                                {{else if eq .Status "offline"}}<span class="tag danger">{{.Status}}</span>
                                {{else if .Status}}<span class="tag">{{.Status}}</span>
                                {{else}}<span class="secondary">-</span>{{end}}
                            </td>
                            <td>
                                {{if eq .BufferState "FULL"}}<span class="tag success">{{.BufferState}}</span>
                                {{else if .BufferState}}<span class="tag warning">{{.BufferState}}</span>
                                {{else}}<span class="secondary">-</span>{{end}}
                            </td>
                            <td>{{.Viewers}}</td>
                            <td>{{.TotalConnections}}</td>
                            <td>{{.Inputs}}</td>
                            <td>{{.BytesUpStr}}</td>
                            <td>{{.BytesDownStr}}</td>
                            <td>{{if .Replicated}}<span class="tag success">Yes</span>{{else}}<span class="tag">No</span>{{end}}</td>
                            <td class="secondary">{{.LastUpdateAgo}}</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>
        {{end}}

        <!-- DB Artifacts Section -->
        <div class="section-title">Database Artifacts ({{.TotalDBArtifacts}})</div>
        <div class="slab">
            <div class="slab-body" style="padding: 0; overflow-x: auto;">
                {{if .DBArtifacts}}
                <table>
                    <thead>
                        <tr>
                            <th>Hash</th>
                            <th>Type</th>
                            <th>Status</th>
                            <th>Storage</th>
                            <th>Sync</th>
                            <th>Stream</th>
                            <th>Format</th>
                            <th>Size</th>
                            <th>Access</th>
                            <th>Last Access</th>
                            <th>Duration</th>
                            <th>Codec</th>
                            <th>Resolution</th>
                            <th>Nodes</th>
                            <th>Created</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range .DBArtifacts}}
                        <tr>
                            <td class="mono accent" style="font-size: 10px;">{{.ArtifactHash}}</td>
                            <td>
                                {{if eq .ArtifactType "clip"}}<span class="tag">clip</span>
                                {{else if eq .ArtifactType "dvr"}}<span class="tag purple">dvr</span>
                                {{else if eq .ArtifactType "vod"}}<span class="tag warning">vod</span>
                                {{else}}<span class="tag">{{.ArtifactType}}</span>{{end}}
                            </td>
                            <td>
                                {{if eq .Status "ready"}}<span class="tag success">{{.Status}}</span>
                                {{else if eq .Status "recording"}}<span class="tag warning">{{.Status}}</span>
                                {{else if eq .Status "processing"}}<span class="tag warning">{{.Status}}</span>
                                {{else if eq .Status "failed"}}<span class="tag danger">{{.Status}}</span>
                                {{else}}<span class="tag">{{.Status}}</span>{{end}}
                            </td>
                            <td>
                                {{if eq .StorageLocation "s3"}}<span class="tag success">{{.StorageLocation}}</span>
                                {{else if eq .StorageLocation "local"}}<span class="tag">{{.StorageLocation}}</span>
                                {{else if eq .StorageLocation "freezing"}}<span class="tag warning">{{.StorageLocation}}</span>
                                {{else}}<span class="tag secondary">{{.StorageLocation}}</span>{{end}}
                            </td>
                            <td>
                                {{if eq .SyncStatus "synced"}}<span class="tag success">{{.SyncStatus}}</span>
                                {{else if eq .SyncStatus "in_progress"}}<span class="tag warning">{{.SyncStatus}}</span>
                                {{else if eq .SyncStatus "failed"}}<span class="tag danger">{{.SyncStatus}}</span>
                                {{else}}<span class="tag secondary">{{.SyncStatus}}</span>{{end}}
                            </td>
                            <td class="mono">{{if .InternalName}}{{.InternalName}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .Format}}<span class="tag">{{.Format}}</span>{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .SizeStr}}{{.SizeStr}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{.AccessCount}}</td>
                            <td>{{if .LastAccessed}}{{.LastAccessed}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .DurationSeconds}}{{.DurationSeconds}}s{{else if .DurationMs}}{{.DurationMs}}ms{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .VideoCodec}}{{.VideoCodec}}{{if .AudioCodec}}/{{.AudioCodec}}{{end}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .Resolution}}{{.Resolution}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .NodeIDs}}{{len .NodeIDs}}{{else}}0{{end}}</td>
                            <td class="secondary" style="font-size: 10px;">{{.CreatedAt}}</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
                {{else}}
                <div style="padding: 20px; text-align: center; color: var(--text-secondary);">No artifacts in database</div>
                {{end}}
            </div>
        </div>

        <!-- Processing Jobs Section -->
        {{if .ProcessingJobs}}
        <div class="section-title">Processing Jobs ({{.TotalProcessingJobs}})</div>
        <div class="slab">
            <div class="slab-body" style="padding: 0; overflow-x: auto;">
                <table>
                    <thead>
                        <tr>
                            <th>Job ID</th>
                            <th>Type</th>
                            <th>Status</th>
                            <th>Progress</th>
                            <th>Artifact</th>
                            <th>Gateway</th>
                            <th>Node</th>
                            <th>Retries</th>
                            <th>Created</th>
                            <th>Started</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range .ProcessingJobs}}
                        <tr>
                            <td class="mono accent" style="font-size: 10px;">{{.JobID}}</td>
                            <td><span class="tag">{{.JobType}}</span></td>
                            <td>
                                {{if eq .Status "completed"}}<span class="tag success">{{.Status}}</span>
                                {{else if eq .Status "processing"}}<span class="tag warning">{{.Status}}</span>
                                {{else if eq .Status "queued"}}<span class="tag">{{.Status}}</span>
                                {{else if eq .Status "failed"}}<span class="tag danger">{{.Status}}</span>
                                {{else}}<span class="tag">{{.Status}}</span>{{end}}
                            </td>
                            <td>{{.Progress}}%</td>
                            <td class="mono" style="font-size: 10px;">{{if .ArtifactHash}}{{.ArtifactHash}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{if .UseGateway}}<span class="tag success">Yes</span>{{else}}<span class="tag">No</span>{{end}}</td>
                            <td class="mono">{{if .ProcessingNode}}{{.ProcessingNode}}{{else}}<span class="secondary">-</span>{{end}}</td>
                            <td>{{.RetryCount}}</td>
                            <td class="secondary" style="font-size: 10px;">{{.CreatedAt}}</td>
                            <td class="secondary" style="font-size: 10px;">{{if .StartedAt}}{{.StartedAt}}{{else}}-{{end}}</td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>
        {{end}}

        <div class="timestamp">
            Page generated at {{.GeneratedAt}}<br>
            Auto-refreshes every 10 seconds
        </div>
    </div>
</body>
</html>`

	// Prepare template data structures
	type StreamInfo struct {
		Name         string
		Total        uint64
		Inputs       uint32
		Bandwidth    uint32
		BandwidthStr string
		BytesUp      uint64
		BytesUpStr   string
		BytesDown    uint64
		BytesDownStr string
		Replicated   bool
	}

	type ArtifactInfo struct {
		ClipHash     string
		StreamName   string
		FilePath     string
		SizeBytes    uint64
		SizeStr      string
		Format       string
		AccessCount  uint64
		LastAccessed string
	}

	type NodeData struct {
		NodeID              string
		Host                string
		IsActive            bool
		CPUPercent          float64
		CPUTenths           uint64
		RAMPercent          uint64
		RAMCurrent          float64
		RAMCurrentStr       string
		RAMMax              float64
		RAMMaxStr           string
		DiskTotalBytesStr   string
		DiskUsedBytesStr    string
		DiskUsedPercent     uint64
		StorageCapacity     uint64
		StorageCapacityStr  string
		StorageUsed         uint64
		StorageUsedStr      string
		GeoLatitude         float64
		GeoLongitude        float64
		LocationName        string
		Capabilities        []string
		Roles               []string
		LastUpdateAgo       string
		Tags                []string
		ConfigStreams       []string
		Port                int
		DTSCPort            int
		StorageLocal        string
		StorageBucket       string
		StoragePrefix       string
		GPUVendor           string
		GPUCount            int
		GPUMemMB            int
		GPUCC               string
		MaxTranscodes       int
		CurrentTranscodes   int
		UpSpeed             uint64
		UpSpeedStr          string
		DownSpeed           uint64
		DownSpeedStr        string
		BWLimit             float64
		BWLimitStr          string
		AvailBandwidth      uint64
		AvailBandwidthStr   string
		AddBandwidth        uint64
		AddBandwidthStr     string
		PendingRedirects    int
		EstBandwidthPerUser uint64
		EstBandwidthStr     string
		Streams             []StreamInfo
		Artifacts           []ArtifactInfo
	}

	type StreamStateData struct {
		InternalName     string
		Status           string
		BufferState      string
		Viewers          int
		TotalConnections int
		Inputs           int
		BytesUp          int64
		BytesUpStr       string
		BytesDown        int64
		BytesDownStr     string
		NodeID           string
		TenantID         string
		StartedAt        string
		LastUpdateAgo    string
		Tracks           []state.StreamTrack
	}

	type StreamInstanceData struct {
		InternalName     string
		NodeID           string
		TenantID         string
		Status           string
		BufferState      string
		Viewers          int
		TotalConnections int
		Inputs           int
		BytesUp          int64
		BytesUpStr       string
		BytesDown        int64
		BytesDownStr     string
		Replicated       bool
		LastUpdateAgo    string
	}

	// DB Artifact data (from foghorn.artifacts + vod_metadata)
	type DBArtifactData struct {
		ArtifactHash    string
		ArtifactType    string
		Status          string
		InternalName    string
		TenantID        string
		StorageLocation string
		SyncStatus      string
		S3URL           string
		Format          string
		SizeBytes       int64
		SizeStr         string
		AccessCount     uint64
		LastAccessed    string
		ManifestPath    string
		DurationSeconds int
		DtshSynced      bool
		RetentionUntil  string
		CreatedAt       string
		// VOD metadata
		VideoCodec  string
		AudioCodec  string
		Resolution  string
		DurationMs  int
		BitrateKbps int
		Filename    string
		Title       string
		// Node distribution
		NodeIDs []string
	}

	// Processing job data (from foghorn.processing_jobs)
	type ProcessingJobData struct {
		JobID          string
		TenantID       string
		ArtifactHash   string
		JobType        string
		Status         string
		Progress       int
		UseGateway     bool
		ProcessingNode string
		RoutingReason  string
		ErrorMessage   string
		RetryCount     int
		CreatedAt      string
		StartedAt      string
		CompletedAt    string
	}

	var nodeData []NodeData
	for _, node := range nodes {
		// Build capabilities list
		var caps []string
		if node.CapIngest {
			caps = append(caps, "Ingest")
		}
		if node.CapEdge {
			caps = append(caps, "Edge")
		}
		if node.CapStorage {
			caps = append(caps, "Storage")
		}
		if node.CapProcessing {
			caps = append(caps, "Processing")
		}

		// Calculate time since last update
		updateAgo := "never"
		if !node.LastUpdate.IsZero() {
			updateAgo = time.Since(node.LastUpdate).Truncate(time.Second).String() + " ago"
		}

		// Build streams list with full info
		var streamList []StreamInfo
		for name, stream := range node.Streams {
			streamList = append(streamList, StreamInfo{
				Name:         name,
				Total:        stream.Total,
				Inputs:       stream.Inputs,
				Bandwidth:    stream.Bandwidth,
				BandwidthStr: formatBytesPerSec(uint64(stream.Bandwidth)),
				BytesUp:      stream.BytesUp,
				BytesUpStr:   formatBytes(stream.BytesUp),
				BytesDown:    stream.BytesDown,
				BytesDownStr: formatBytes(stream.BytesDown),
				Replicated:   stream.Replicated,
			})
		}
		// Sort streams by name
		sort.Slice(streamList, func(i, j int) bool {
			return streamList[i].Name < streamList[j].Name
		})

		// Build artifacts list
		var artifactList []ArtifactInfo
		for _, a := range node.Artifacts {
			lastAccessed := "-"
			if ts := a.GetLastAccessed(); ts > 0 {
				lastAccessed = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
			}
			artifactList = append(artifactList, ArtifactInfo{
				ClipHash:     a.GetClipHash(),
				StreamName:   a.GetStreamName(),
				FilePath:     a.GetFilePath(),
				SizeBytes:    uint64(a.GetSizeBytes()),
				SizeStr:      formatBytes(uint64(a.GetSizeBytes())),
				Format:       a.GetFormat(),
				AccessCount:  a.GetAccessCount(),
				LastAccessed: lastAccessed,
			})
		}

		// Disk usage
		diskPercent := uint64(0)
		if node.DiskTotalBytes > 0 {
			diskPercent = (node.DiskUsedBytes * 100) / node.DiskTotalBytes
		}

		nodeData = append(nodeData, NodeData{
			NodeID:     node.NodeID,
			Host:       node.Host,
			IsActive:   node.IsActive,
			CPUPercent: node.CPU,
			CPUTenths:  uint64(node.CPU * 10),
			RAMPercent: func() uint64 {
				if node.RAMMax > 0 {
					return uint64((node.RAMCurrent * 100) / node.RAMMax)
				}
				return 0
			}(),
			RAMCurrent:          node.RAMCurrent,
			RAMCurrentStr:       formatBytes(uint64(node.RAMCurrent)),
			RAMMax:              node.RAMMax,
			RAMMaxStr:           formatBytes(uint64(node.RAMMax)),
			DiskTotalBytesStr:   formatBytes(node.DiskTotalBytes),
			DiskUsedBytesStr:    formatBytes(node.DiskUsedBytes),
			DiskUsedPercent:     diskPercent,
			StorageCapacity:     node.StorageCapacityBytes,
			StorageCapacityStr:  formatBytes(node.StorageCapacityBytes),
			StorageUsed:         node.StorageUsedBytes,
			StorageUsedStr:      formatBytes(node.StorageUsedBytes),
			GeoLatitude:         node.GeoLatitude,
			GeoLongitude:        node.GeoLongitude,
			LocationName:        node.LocationName,
			Capabilities:        caps,
			Roles:               node.Roles,
			LastUpdateAgo:       updateAgo,
			Tags:                node.Tags,
			ConfigStreams:       node.ConfigStreams,
			Port:                node.Port,
			DTSCPort:            node.DTSCPort,
			StorageLocal:        node.StorageLocal,
			StorageBucket:       node.StorageBucket,
			StoragePrefix:       node.StoragePrefix,
			GPUVendor:           node.GPUVendor,
			GPUCount:            node.GPUCount,
			GPUMemMB:            node.GPUMemMB,
			GPUCC:               node.GPUCC,
			MaxTranscodes:       node.MaxTranscodes,
			CurrentTranscodes:   node.CurrentTranscodes,
			UpSpeed:             uint64(node.UpSpeed),
			UpSpeedStr:          formatBytesPerSec(uint64(node.UpSpeed)),
			DownSpeed:           uint64(node.DownSpeed),
			DownSpeedStr:        formatBytesPerSec(uint64(node.DownSpeed)),
			BWLimit:             node.BWLimit,
			BWLimitStr:          formatBytesPerSec(uint64(node.BWLimit)),
			AvailBandwidth:      node.AvailBandwidth,
			AvailBandwidthStr:   formatBytesPerSec(uint64(node.AvailBandwidth)),
			AddBandwidth:        node.AddBandwidth,
			AddBandwidthStr:     formatBytesPerSec(node.AddBandwidth),
			PendingRedirects:    node.PendingRedirects,
			EstBandwidthPerUser: node.EstBandwidthPerUser,
			EstBandwidthStr:     formatBytesPerSec(node.EstBandwidthPerUser),
			Streams:             streamList,
			Artifacts:           artifactList,
		})
	}

	var streamStateData []StreamStateData
	for _, stream := range streamStates {
		sourceNodeID := findStreamSourceNodeID(stream, streamInstances)
		updateAgo := "never"
		if !stream.LastUpdate.IsZero() {
			updateAgo = time.Since(stream.LastUpdate).Truncate(time.Second).String() + " ago"
		}
		startedAt := ""
		if stream.StartedAt != nil {
			startedAt = stream.StartedAt.Format("15:04:05")
		}

		streamStateData = append(streamStateData, StreamStateData{
			InternalName:     stream.InternalName,
			Status:           stream.Status,
			BufferState:      stream.BufferState,
			Viewers:          stream.Viewers,
			TotalConnections: stream.TotalConnections,
			Inputs:           stream.Inputs,
			BytesUp:          stream.BytesUp,
			BytesUpStr:       formatBytes(uint64(stream.BytesUp)),
			BytesDown:        stream.BytesDown,
			BytesDownStr:     formatBytes(uint64(stream.BytesDown)),
			NodeID:           sourceNodeID,
			TenantID:         stream.TenantID,
			StartedAt:        startedAt,
			LastUpdateAgo:    updateAgo,
			Tracks:           stream.Tracks,
		})
	}

	// Build stream instances list
	var streamInstanceData []StreamInstanceData
	for internalName, nodeMap := range streamInstances {
		for nodeID, inst := range nodeMap {
			updateAgo := "never"
			if !inst.LastUpdate.IsZero() {
				updateAgo = time.Since(inst.LastUpdate).Truncate(time.Second).String() + " ago"
			}
			streamInstanceData = append(streamInstanceData, StreamInstanceData{
				InternalName:     internalName,
				NodeID:           nodeID,
				TenantID:         inst.TenantID,
				Status:           inst.Status,
				BufferState:      inst.BufferState,
				Viewers:          inst.Viewers,
				TotalConnections: inst.TotalConnections,
				Inputs:           inst.Inputs,
				BytesUp:          inst.BytesUp,
				BytesUpStr:       formatBytes(uint64(inst.BytesUp)),
				BytesDown:        inst.BytesDown,
				BytesDownStr:     formatBytes(uint64(inst.BytesDown)),
				Replicated:       inst.Replicated,
				LastUpdateAgo:    updateAgo,
			})
		}
	}
	// Sort by stream name then node
	sort.Slice(streamInstanceData, func(i, j int) bool {
		if streamInstanceData[i].InternalName != streamInstanceData[j].InternalName {
			return streamInstanceData[i].InternalName < streamInstanceData[j].InternalName
		}
		return streamInstanceData[i].NodeID < streamInstanceData[j].NodeID
	})

	// Query DB artifacts and processing jobs
	var dbArtifacts []DBArtifactData
	var processingJobs []ProcessingJobData

	if db != nil {
		// Query all artifacts with vod_metadata
		artifactRows, err := db.Query(`
			SELECT
				a.artifact_hash, a.artifact_type, a.status, a.internal_name, a.tenant_id,
				a.storage_location, a.sync_status, a.s3_url, a.format, a.size_bytes,
				a.access_count, a.last_accessed_at,
				a.manifest_path, a.duration_seconds, a.dtsh_synced, a.retention_until,
				a.created_at,
				v.video_codec, v.audio_codec, v.resolution, v.duration_ms, v.bitrate_kbps,
				v.filename, v.title
			FROM foghorn.artifacts a
			LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
			WHERE a.status != 'deleted'
			ORDER BY a.created_at DESC
			LIMIT 200
		`)
		if err == nil {
			defer artifactRows.Close()
			for artifactRows.Next() {
				var hash, artType, status, storageLocation, syncStatus string
				var internalName, tenantID, s3URL, format, manifestPath, retentionUntil sql.NullString
				var sizeBytes sql.NullInt64
				var accessCount sql.NullInt64
				var lastAccessed sql.NullTime
				var durationSeconds sql.NullInt32
				var dtshSynced sql.NullBool
				var createdAt time.Time
				var videoCodec, audioCodec, resolution, filename, title sql.NullString
				var durationMs, bitrateKbps sql.NullInt32

				errScan := artifactRows.Scan(
					&hash, &artType, &status, &internalName, &tenantID,
					&storageLocation, &syncStatus, &s3URL, &format, &sizeBytes,
					&accessCount, &lastAccessed,
					&manifestPath, &durationSeconds, &dtshSynced, &retentionUntil,
					&createdAt,
					&videoCodec, &audioCodec, &resolution, &durationMs, &bitrateKbps,
					&filename, &title,
				)
				if errScan != nil {
					continue
				}

				art := DBArtifactData{
					ArtifactHash:    hash,
					ArtifactType:    artType,
					Status:          status,
					StorageLocation: storageLocation,
					SyncStatus:      syncStatus,
					CreatedAt:       createdAt.Format("2006-01-02 15:04:05"),
				}
				if internalName.Valid {
					art.InternalName = internalName.String
				}
				if tenantID.Valid {
					art.TenantID = tenantID.String
				}
				if s3URL.Valid {
					art.S3URL = s3URL.String
				}
				if format.Valid {
					art.Format = format.String
				}
				if sizeBytes.Valid {
					art.SizeBytes = sizeBytes.Int64
					art.SizeStr = formatBytes(uint64(sizeBytes.Int64))
				}
				if accessCount.Valid && accessCount.Int64 >= 0 {
					art.AccessCount = uint64(accessCount.Int64)
				}
				if lastAccessed.Valid {
					art.LastAccessed = lastAccessed.Time.Format("2006-01-02 15:04:05")
				}
				if manifestPath.Valid {
					art.ManifestPath = manifestPath.String
				}
				if durationSeconds.Valid {
					art.DurationSeconds = int(durationSeconds.Int32)
				}
				if dtshSynced.Valid {
					art.DtshSynced = dtshSynced.Bool
				}
				if retentionUntil.Valid {
					art.RetentionUntil = retentionUntil.String
				}
				// VOD metadata
				if videoCodec.Valid {
					art.VideoCodec = videoCodec.String
				}
				if audioCodec.Valid {
					art.AudioCodec = audioCodec.String
				}
				if resolution.Valid {
					art.Resolution = resolution.String
				}
				if durationMs.Valid {
					art.DurationMs = int(durationMs.Int32)
				}
				if bitrateKbps.Valid {
					art.BitrateKbps = int(bitrateKbps.Int32)
				}
				if filename.Valid {
					art.Filename = filename.String
				}
				if title.Valid {
					art.Title = title.String
				}

				// Query nodes hosting this artifact
				art.NodeIDs = func() []string {
					nodeRows, errQuery := db.Query(`
						SELECT node_id FROM foghorn.artifact_nodes
						WHERE artifact_hash = $1 AND NOT is_orphaned
					`, hash)
					if errQuery != nil {
						return nil
					}
					defer nodeRows.Close()
					var ids []string
					for nodeRows.Next() {
						var nodeID string
						if errScan := nodeRows.Scan(&nodeID); errScan == nil {
							ids = append(ids, nodeID)
						}
					}
					return ids
				}()

				dbArtifacts = append(dbArtifacts, art)
			}
		}

		// Query processing jobs
		jobRows, err := db.Query(`
			SELECT
				job_id, tenant_id, artifact_hash, job_type, status, progress,
				use_gateway, processing_node_id, routing_reason, error_message, retry_count,
				created_at, started_at, completed_at
			FROM foghorn.processing_jobs
			WHERE status NOT IN ('completed', 'failed') OR created_at > NOW() - INTERVAL '1 hour'
			ORDER BY created_at DESC
			LIMIT 50
		`)
		if err == nil {
			defer jobRows.Close()
			for jobRows.Next() {
				var jobID, tenantID, jobType, status string
				var artifactHash, processingNode, routingReason, errorMessage sql.NullString
				var progress, retryCount int
				var useGateway bool
				var createdAt time.Time
				var startedAt, completedAt sql.NullTime

				err := jobRows.Scan(
					&jobID, &tenantID, &artifactHash, &jobType, &status, &progress,
					&useGateway, &processingNode, &routingReason, &errorMessage, &retryCount,
					&createdAt, &startedAt, &completedAt,
				)
				if err != nil {
					continue
				}

				job := ProcessingJobData{
					JobID:      jobID,
					TenantID:   tenantID,
					JobType:    jobType,
					Status:     status,
					Progress:   progress,
					UseGateway: useGateway,
					RetryCount: retryCount,
					CreatedAt:  createdAt.Format("2006-01-02 15:04:05"),
				}
				if artifactHash.Valid {
					job.ArtifactHash = artifactHash.String
				}
				if processingNode.Valid {
					job.ProcessingNode = processingNode.String
				}
				if routingReason.Valid {
					job.RoutingReason = routingReason.String
				}
				if errorMessage.Valid {
					job.ErrorMessage = errorMessage.String
				}
				if startedAt.Valid {
					job.StartedAt = startedAt.Time.Format("2006-01-02 15:04:05")
				}
				if completedAt.Valid {
					job.CompletedAt = completedAt.Time.Format("2006-01-02 15:04:05")
				}

				processingJobs = append(processingJobs, job)
			}
		}
	}

	// Virtual viewer stats for template
	type VirtualViewerStatsData struct {
		TotalViewers        int
		TotalPending        int
		TotalActive         int
		TotalAbandoned      int
		TotalDisconnected   int
		EstPendingBandwidth string
	}

	vvStats := VirtualViewerStatsData{
		TotalViewers:        safeInt(virtualViewerStats["total_viewers"]),
		TotalPending:        safeInt(virtualViewerStats["pending"]),
		TotalActive:         safeInt(virtualViewerStats["active"]),
		TotalAbandoned:      safeInt(virtualViewerStats["abandoned"]),
		TotalDisconnected:   safeInt(virtualViewerStats["disconnected"]),
		EstPendingBandwidth: formatBytesPerSec(safeUint64(virtualViewerStats["est_pending_bandwidth"])),
	}

	type StreamContextCacheEntryData struct {
		Key             string
		TenantID        string
		UserID          string
		Source          string
		UpdatedAgo      string
		UpdatedAt       time.Time
		TenantIsMissing bool
	}
	type StreamContextCacheData struct {
		Enabled        bool
		Size           int
		Hits           uint64
		Misses         uint64
		ResolveErrors  uint64
		LastResolveAgo string
		LastError      string
		MissingTenant  int
		Entries        []StreamContextCacheEntryData
	}

	streamCtxCache := StreamContextCacheData{Enabled: false}
	if triggerProcessor != nil {
		snap := triggerProcessor.StreamContextCacheSnapshot()
		streamCtxCache.Enabled = true
		streamCtxCache.Size = snap.Size
		streamCtxCache.Hits = snap.Hits
		streamCtxCache.Misses = snap.Misses
		streamCtxCache.ResolveErrors = snap.ResErrors
		streamCtxCache.LastError = snap.LastError
		if snap.LastResolve.IsZero() {
			streamCtxCache.LastResolveAgo = "never"
		} else {
			streamCtxCache.LastResolveAgo = time.Since(snap.LastResolve).Truncate(time.Second).String() + " ago"
		}

		zeroUUID := "00000000-0000-0000-0000-000000000000"
		entries := make([]StreamContextCacheEntryData, 0, len(snap.Entries))
		for _, e := range snap.Entries {
			tid := e.TenantID
			tenantMissing := tid == "" || tid == zeroUUID
			if tenantMissing {
				streamCtxCache.MissingTenant++
			}
			updatedAgo := ""
			if !e.UpdatedAt.IsZero() {
				updatedAgo = time.Since(e.UpdatedAt).Truncate(time.Second).String() + " ago"
			}
			entries = append(entries, StreamContextCacheEntryData{
				Key:             e.Key,
				TenantID:        tid,
				UserID:          e.UserID,
				Source:          e.Source,
				UpdatedAgo:      updatedAgo,
				UpdatedAt:       e.UpdatedAt,
				TenantIsMissing: tenantMissing,
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		})
		// Show only the most recent 50 entries (cache can grow large in long-running dev sessions)
		if len(entries) > 50 {
			entries = entries[:50]
		}
		streamCtxCache.Entries = entries
	}

	// Template data
	templateData := struct {
		GeneratedAt         string
		TotalNodes          int
		ActiveNodes         int
		TotalStreams        int
		TotalViewers        uint64
		TotalArtifacts      int
		TotalDBArtifacts    int
		TotalProcessingJobs int
		CPUWeight           uint64
		RAMWeight           uint64
		BWWeight            uint64
		GeoWeight           uint64
		StreamBonus         uint64
		VirtualViewerStats  VirtualViewerStatsData
		StreamContextCache  StreamContextCacheData
		Nodes               []NodeData
		StreamStates        []StreamStateData
		StreamInstances     []StreamInstanceData
		DBArtifacts         []DBArtifactData
		ProcessingJobs      []ProcessingJobData
	}{
		GeneratedAt:         generatedAt.Format("2006-01-02 15:04:05 MST"),
		TotalNodes:          len(nodes),
		ActiveNodes:         activeNodes,
		TotalStreams:        totalStreams,
		TotalViewers:        totalViewers,
		TotalArtifacts:      totalArtifacts,
		TotalDBArtifacts:    len(dbArtifacts),
		TotalProcessingJobs: len(processingJobs),
		CPUWeight:           weights["cpu"],
		RAMWeight:           weights["ram"],
		BWWeight:            weights["bw"],
		GeoWeight:           weights["geo"],
		StreamBonus:         weights["bonus"],
		VirtualViewerStats:  vvStats,
		StreamContextCache:  streamCtxCache,
		Nodes:               nodeData,
		StreamStates:        streamStateData,
		StreamInstances:     streamInstanceData,
		DBArtifacts:         dbArtifacts,
		ProcessingJobs:      processingJobs,
	}

	// Parse and execute template
	tmpl, err := template.New("root").Parse(htmlTemplate)
	if err != nil {
		logger.WithError(err).Error("Failed to parse HTML template")
		c.String(http.StatusInternalServerError, "Template error: %v", err)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")

	if err := tmpl.Execute(c.Writer, templateData); err != nil {
		logger.WithError(err).Error("Failed to execute HTML template")
		c.String(http.StatusInternalServerError, "Template execution error: %v", err)
		return
	}
}
