package heartbeat

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

func renderInfraAlertEmail(alerts []InfraAlert) (string, error) {
	if len(alerts) == 0 {
		return "", fmt.Errorf("no alerts to render")
	}

	severity := "WARNING"
	for _, a := range alerts {
		if a.Severity() == "CRITICAL" {
			severity = "CRITICAL"
			break
		}
	}

	data := infraEmailData{
		Severity:    severity,
		ClusterName: alerts[0].ClusterName,
		ClusterID:   alerts[0].ClusterID,
		NodeID:      alerts[0].NodeID,
		Alerts:      alerts,
		DetectedAt:  alerts[0].DetectedAt.UTC(),
		ActionItems: collectActionItems(alerts),
	}

	funcs := template.FuncMap{
		"formatPercent": func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
		"hasBaseline":   func(a InfraAlert) bool { return a.Baseline > 0 },
		"severityColor": func(a InfraAlert) string {
			if a.Severity() == "CRITICAL" {
				return "#e74c3c"
			}
			return "#e67e22"
		},
		"alertLabel": func(a InfraAlert) string {
			switch a.AlertType {
			case InfraAlertCPU:
				return "CPU Usage"
			case InfraAlertMemory:
				return "Memory Usage"
			case InfraAlertDiskWarning:
				return "Disk Usage"
			case InfraAlertDiskCritical:
				return "Disk Usage"
			default:
				return string(a.AlertType)
			}
		},
	}

	tpl, err := template.New("infra_alert").Funcs(funcs).Parse(infraAlertTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type infraEmailData struct {
	Severity    string
	ClusterName string
	ClusterID   string
	NodeID      string
	Alerts      []InfraAlert
	DetectedAt  time.Time
	ActionItems []string
}

func collectActionItems(alerts []InfraAlert) []string {
	seen := make(map[InfraAlertType]bool)
	var items []string
	for _, a := range alerts {
		if seen[a.AlertType] {
			continue
		}
		seen[a.AlertType] = true
		items = append(items, actionItemsFor(a.AlertType)...)
	}
	return items
}

func actionItemsFor(alertType InfraAlertType) []string {
	switch alertType {
	case InfraAlertCPU:
		return []string{"Known MistServer issue. Restart the MistServer process or reboot the node."}
	case InfraAlertMemory:
		return []string{"Check for memory leaks. Consider restarting services on the node."}
	case InfraAlertDiskWarning:
		return []string{"Free up disk space or expand storage. Recording and DVR may fail."}
	case InfraAlertDiskCritical:
		return []string{"Immediate action required. The node may become unresponsive if disk fills completely."}
	default:
		return nil
	}
}

func infraAlertSubject(alerts []InfraAlert) string {
	if len(alerts) == 0 {
		return "[FrameWorks] Infrastructure Alert"
	}
	severity := "WARNING"
	for _, a := range alerts {
		if a.Severity() == "CRITICAL" {
			severity = "CRITICAL"
			break
		}
	}
	a := alerts[0]
	issues := make([]string, 0, len(alerts))
	for _, al := range alerts {
		switch al.AlertType {
		case InfraAlertCPU:
			issues = append(issues, "CPU stuck")
		case InfraAlertMemory:
			issues = append(issues, "memory exhaustion")
		case InfraAlertDiskWarning:
			issues = append(issues, "disk warning")
		case InfraAlertDiskCritical:
			issues = append(issues, "disk critical")
		}
	}
	return fmt.Sprintf("[FrameWorks] Infrastructure Alert: %s on %s/%s - %s",
		severity, a.ClusterName, a.NodeID, strings.Join(issues, ", "))
}

const infraAlertTemplate = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Infrastructure Alert</title></head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333; margin: 0; padding: 0;">
<div style="max-width: 640px; margin: 0 auto; padding: 24px;">

{{if eq .Severity "CRITICAL"}}
<div style="background-color: #e74c3c; color: white; padding: 14px 20px; border-radius: 6px; margin-bottom: 20px;">
    <strong>CRITICAL Infrastructure Alert</strong>
</div>
{{else}}
<div style="background-color: #e67e22; color: white; padding: 14px 20px; border-radius: 6px; margin-bottom: 20px;">
    <strong>Infrastructure Warning</strong>
</div>
{{end}}

<p>An infrastructure issue was detected on your cluster.</p>

<table style="width: 100%; border-collapse: collapse; margin: 20px 0;">
    <tr style="background-color: #eef1f5;">
        <th style="padding: 10px; text-align: left; border-bottom: 2px solid #ddd;">Cluster</th>
        <td style="padding: 10px; border-bottom: 2px solid #ddd;"><strong>{{.ClusterName}}</strong> ({{.ClusterID}})</td>
    </tr>
    <tr>
        <th style="padding: 10px; text-align: left; border-bottom: 1px solid #eee;">Node</th>
        <td style="padding: 10px; border-bottom: 1px solid #eee;"><code>{{.NodeID}}</code></td>
    </tr>
</table>

<h3 style="color: #2c3e50; margin-top: 24px;">Issues Detected</h3>
<table style="width: 100%; border-collapse: collapse; margin-bottom: 20px;">
    <tr style="background-color: #eef1f5;">
        <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Resource</th>
        <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Current</th>
        <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Threshold</th>
        <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Status</th>
    </tr>
    {{range .Alerts}}
    <tr>
        <td style="padding: 10px; border-bottom: 1px solid #eee;">{{alertLabel .}}</td>
        <td style="padding: 10px; border-bottom: 1px solid #eee;"><strong>{{formatPercent .Current}}</strong></td>
        <td style="padding: 10px; border-bottom: 1px solid #eee;">{{formatPercent .Threshold}}</td>
        <td style="padding: 10px; border-bottom: 1px solid #eee; color: {{severityColor .}}; font-weight: bold;">{{.Severity}}</td>
    </tr>
    {{if hasBaseline .}}
    <tr style="background-color: #fafafa;">
        <td colspan="4" style="padding: 6px 10px; border-bottom: 1px solid #eee; color: #6c757d; font-size: 12px;">
            Baseline average: {{formatPercent .Baseline}}
        </td>
    </tr>
    {{end}}
    {{end}}
</table>

{{if .ActionItems}}
<h3 style="color: #2c3e50; margin-top: 24px;">What To Do</h3>
<ul style="padding-left: 20px;">
    {{range .ActionItems}}
    <li style="margin-bottom: 8px;">{{.}}</li>
    {{end}}
</ul>
{{end}}

<p style="color: #6c757d; font-size: 12px; margin-top: 30px;">
    Detected at {{.DetectedAt.Format "January 2, 2006 at 3:04 PM UTC"}}<br>
    This alert will not repeat for 4 hours.
</p>

</div>
</body>
</html>`
