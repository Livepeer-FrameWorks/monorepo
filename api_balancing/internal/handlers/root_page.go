package handlers

import (
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/gin-gonic/gin"
)

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

	for _, node := range nodes {
		if node.IsActive {
			activeNodes++
		}
		totalViewers += getTotalViewers(node)
		totalStreams += len(node.Streams)
	}

	// HTML template with inline CSS
	htmlTemplate := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Foghorn - Internal State</title>
    <meta http-equiv="refresh" content="30">
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
            background: #0d1117;
            color: #f0f6fc;
            line-height: 1.4;
            padding: 20px;
            font-size: 13px;
        }
        .container {
            max-width: 1400px;
            margin: 0 auto;
        }
        h1 {
            color: #58a6ff;
            margin-bottom: 20px;
            font-size: 24px;
            text-align: center;
            border-bottom: 2px solid #21262d;
            padding-bottom: 10px;
        }
        h2 {
            color: #79c0ff;
            margin: 30px 0 15px 0;
            font-size: 18px;
            border-left: 4px solid #58a6ff;
            padding-left: 10px;
        }
        h3 {
            color: #a5a5a5;
            margin: 20px 0 10px 0;
            font-size: 14px;
        }
        .info-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
            gap: 20px;
            margin-bottom: 30px;
        }
        .info-box {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 6px;
            padding: 15px;
        }
        .stat-row {
            display: flex;
            justify-content: space-between;
            margin: 8px 0;
            padding: 4px 0;
            border-bottom: 1px solid #21262d;
        }
        .stat-label {
            color: #8b949e;
            font-weight: bold;
        }
        .stat-value {
            color: #f0f6fc;
            font-family: monospace;
        }
        .node-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
            gap: 15px;
            margin-bottom: 30px;
        }
        .node-card {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 6px;
            padding: 15px;
        }
        .node-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 10px;
        }
        .node-host {
            color: #58a6ff;
            font-weight: bold;
            font-size: 14px;
        }
        .node-status {
            padding: 2px 6px;
            border-radius: 3px;
            font-size: 11px;
            font-weight: bold;
        }
        .status-active {
            background: #238636;
            color: white;
        }
        .status-inactive {
            background: #da3633;
            color: white;
        }
        .metrics-row {
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 10px;
            margin: 5px 0;
        }
        .metric {
            font-size: 11px;
            color: #8b949e;
        }
        .metric-value {
            color: #f0f6fc;
            font-weight: bold;
        }
        .streams-list {
            margin-top: 10px;
            border-top: 1px solid #21262d;
            padding-top: 8px;
        }
        .stream-item {
            display: flex;
            justify-content: space-between;
            font-size: 11px;
            margin: 3px 0;
            color: #8b949e;
        }
        .stream-name {
            color: #79c0ff;
            max-width: 200px;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .stream-viewers {
            color: #f85149;
            font-weight: bold;
        }
        .stream-states {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(350px, 1fr));
            gap: 15px;
        }
        .stream-state-card {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 6px;
            padding: 12px;
        }
        .stream-title {
            color: #79c0ff;
            font-weight: bold;
            margin-bottom: 8px;
            font-size: 12px;
        }
        .buffer-state {
            padding: 1px 4px;
            border-radius: 2px;
            font-size: 10px;
            font-weight: bold;
            margin-left: 8px;
        }
        .buffer-full { background: #238636; }
        .buffer-empty { background: #da3633; }
        .buffer-dry { background: #d29922; }
        .buffer-recover { background: #1f6feb; }
        .tracks-list {
            margin-top: 8px;
            font-size: 10px;
        }
        .track-item {
            margin: 2px 0;
            color: #8b949e;
        }
        .timestamp {
            text-align: center;
            color: #6e7681;
            font-size: 11px;
            margin-top: 30px;
            padding-top: 20px;
            border-top: 1px solid #21262d;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🌫️ Foghorn Load Balancer - Internal State</h1>
        
        <div class="info-grid">
            <div class="info-box">
                <h3>Cluster Overview</h3>
                <div class="stat-row">
                    <span class="stat-label">Total Nodes:</span>
                    <span class="stat-value">{{.TotalNodes}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Active Nodes:</span>
                    <span class="stat-value">{{.ActiveNodes}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Total Streams:</span>
                    <span class="stat-value">{{.TotalStreams}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Total Viewers:</span>
                    <span class="stat-value">{{.TotalViewers}}</span>
                </div>
            </div>
            
            <div class="info-box">
                <h3>Load Balancer Configuration</h3>
                <div class="stat-row">
                    <span class="stat-label">CPU Weight:</span>
                    <span class="stat-value">{{.CPUWeight}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">RAM Weight:</span>
                    <span class="stat-value">{{.RAMWeight}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Bandwidth Weight:</span>
                    <span class="stat-value">{{.BWWeight}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Geo Weight:</span>
                    <span class="stat-value">{{.GeoWeight}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Stream Bonus:</span>
                    <span class="stat-value">{{.StreamBonus}}</span>
                </div>
            </div>
        </div>

        <h2>📡 Cluster Nodes ({{.TotalNodes}})</h2>
        <div class="node-grid">
            {{range .Nodes}}
            <div class="node-card">
                <div class="node-header">
                    <span class="node-host">{{.Host}}</span>
                    <span class="node-status {{if .IsActive}}status-active{{else}}status-inactive{{end}}">
                        {{if .IsActive}}ACTIVE{{else}}INACTIVE{{end}}
                    </span>
                </div>
                
                <div class="metrics-row">
                    <div class="metric">CPU: <span class="metric-value">{{printf "%.1f" .CPUPercent}}%</span></div>
                    <div class="metric">RAM: <span class="metric-value">{{.RAMPercent}}%</span></div>
                </div>
                
                <div class="metrics-row">
                    <div class="metric">Location: <span class="metric-value">{{.LocationName}}</span></div>
                    <div class="metric">Viewers: <span class="metric-value">{{.TotalViewers}}</span></div>
                </div>
                
                {{if .GeoLatitude}}
                <div class="metrics-row">
                    <div class="metric">Lat: <span class="metric-value">{{printf "%.3f" .GeoLatitude}}</span></div>
                    <div class="metric">Lng: <span class="metric-value">{{printf "%.3f" .GeoLongitude}}</span></div>
                </div>
                {{end}}
                
                <div class="metrics-row">
                    <div class="metric">Capabilities: <span class="metric-value">{{.CapabilitiesStr}}</span></div>
                    <div class="metric">Last Update: <span class="metric-value">{{.LastUpdateAgo}}</span></div>
                </div>
                
                {{if .Streams}}
                <div class="streams-list">
                    <div style="color: #8b949e; font-size: 11px; margin-bottom: 5px;">Active Streams ({{len .Streams}}):</div>
                    {{range .Streams}}
                    <div class="stream-item">
                        <span class="stream-name">{{.Name}}</span>
                        <span class="stream-viewers">{{.Total}} viewers</span>
                    </div>
                    {{end}}
                </div>
                {{end}}
            </div>
            {{end}}
        </div>

        <h2>🎥 Stream States ({{len .StreamStates}})</h2>
        <div class="stream-states">
            {{range .StreamStates}}
            <div class="stream-state-card">
                <div class="stream-title">
                    {{.InternalName}}
                    <span class="buffer-state buffer-{{.BufferStateClass}}">{{.BufferState}}</span>
                </div>
                
                <div class="metrics-row">
                    <div class="metric">Status: <span class="metric-value">{{.Status}}</span></div>
                    <div class="metric">Viewers: <span class="metric-value">{{.Viewers}}</span></div>
                </div>
                
                <div class="metrics-row">
                    <div class="metric">Node: <span class="metric-value">{{.NodeID}}</span></div>
                    <div class="metric">Updated: <span class="metric-value">{{.LastUpdateAgo}}</span></div>
                </div>
                
                {{if .Tracks}}
                <div class="tracks-list">
                    <div style="color: #8b949e; margin-bottom: 3px;">Tracks:</div>
                    {{range .Tracks}}
                    <div class="track-item">
                        {{.TrackID}}: {{.Type}} {{.Codec}} 
                        {{if .Bitrate}}- {{.Bitrate}}kbps{{end}}
                        {{if and .Width .Height}}- {{.Width}}x{{.Height}}{{end}}
                    </div>
                    {{end}}
                </div>
                {{end}}
            </div>
            {{end}}
        </div>

        <div class="timestamp">
            Page generated at {{.GeneratedAt}}<br>
            Auto-refreshes every 30 seconds
        </div>
    </div>
</body>
</html>`

	// Prepare template data
	type NodeData struct {
		Host            string
		IsActive        bool
		CPUPercent      float64
		RAMPercent      uint64
		GeoLatitude     float64
		GeoLongitude    float64
		LocationName    string
		TotalViewers    uint64
		CapabilitiesStr string
		LastUpdateAgo   string
		Streams         []struct {
			Name  string
			Total uint64
		}
	}

	type StreamData struct {
		InternalName     string
		Status           string
		BufferState      string
		BufferStateClass string
		Viewers          int
		NodeID           string
		LastUpdateAgo    string
		Tracks           []state.StreamTrack
	}

	var nodeData []NodeData
	for _, node := range nodes {
		// Build capabilities string
		var caps []string
		if node.CapIngest {
			caps = append(caps, "ingest")
		}
		if node.CapEdge {
			caps = append(caps, "edge")
		}
		if node.CapStorage {
			caps = append(caps, "storage")
		}
		if node.CapProcessing {
			caps = append(caps, "processing")
		}
		capsStr := strings.Join(caps, ", ")
		if capsStr == "" {
			capsStr = "none"
		}

		// Calculate time since last update
		updateAgo := "never"
		if !node.LastUpdate.IsZero() {
			updateAgo = time.Since(node.LastUpdate).Truncate(time.Second).String() + " ago"
		}

		// Build streams list
		var streamList []struct {
			Name  string
			Total uint64
		}
		for name, stream := range node.Streams {
			streamList = append(streamList, struct {
				Name  string
				Total uint64
			}{name, stream.Total})
		}

		nodeData = append(nodeData, NodeData{
			Host:       node.Host,
			IsActive:   node.IsActive,
			CPUPercent: float64(node.CPU) / 10.0,
			RAMPercent: func() uint64 {
				if node.RAMMax > 0 {
					return uint64((node.RAMCurrent * 100) / node.RAMMax)
				}
				return 0
			}(),
			GeoLatitude:     node.GeoLatitude,
			GeoLongitude:    node.GeoLongitude,
			LocationName:    node.LocationName,
			TotalViewers:    getTotalViewers(node),
			CapabilitiesStr: capsStr,
			LastUpdateAgo:   updateAgo,
			Streams:         streamList,
		})
	}

	var streamData []StreamData
	for _, stream := range streamStates {
		// Map buffer state to CSS class
		bufferClass := strings.ToLower(stream.BufferState)

		// Calculate time since last update
		updateAgo := "never"
		if !stream.LastUpdate.IsZero() {
			updateAgo = time.Since(stream.LastUpdate).Truncate(time.Second).String() + " ago"
		}

		streamData = append(streamData, StreamData{
			InternalName:     stream.InternalName,
			Status:           stream.Status,
			BufferState:      stream.BufferState,
			BufferStateClass: bufferClass,
			Viewers:          stream.Viewers,
			NodeID:           stream.NodeID,
			LastUpdateAgo:    updateAgo,
			Tracks:           stream.Tracks,
		})
	}

	// Template data
	templateData := struct {
		GeneratedAt  string
		TotalNodes   int
		ActiveNodes  int
		TotalStreams int
		TotalViewers uint64
		CPUWeight    uint64
		RAMWeight    uint64
		BWWeight     uint64
		GeoWeight    uint64
		StreamBonus  uint64
		Nodes        []NodeData
		StreamStates []StreamData
	}{
		GeneratedAt:  generatedAt.Format("2006-01-02 15:04:05 MST"),
		TotalNodes:   len(nodes),
		ActiveNodes:  activeNodes,
		TotalStreams: totalStreams,
		TotalViewers: totalViewers,
		CPUWeight:    weights["cpu"],
		RAMWeight:    weights["ram"],
		BWWeight:     weights["bw"],
		GeoWeight:    weights["geo"],
		StreamBonus:  weights["bonus"],
		Nodes:        nodeData,
		StreamStates: streamData,
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
