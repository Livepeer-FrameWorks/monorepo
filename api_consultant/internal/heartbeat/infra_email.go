package heartbeat

import (
	"fmt"
	"html/template"
	"os"
	"strings"
	"time"

	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
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

	title := "Infrastructure warning"
	if severity == "CRITICAL" {
		title = "Critical infrastructure alert"
	}
	return emailpkg.RenderLayout(emailpkg.LayoutData{
		LogoURL:      emailpkg.PublicLogoURL(os.Getenv("EMAIL_LOGO_URL"), os.Getenv("WEBAPP_PUBLIC_URL")),
		Preheader:    fmt.Sprintf("%s infrastructure alert for %s/%s.", severity, data.ClusterName, data.NodeID),
		Eyebrow:      "Infrastructure",
		Title:        title,
		SupportEmail: infraSupportEmail(),
		Content:      data,
	}, infraAlertTemplate, funcs)
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
	items = append(items,
		"Run frameworks edge doctor to diagnose the node.",
		"Check frameworks edge logs for service errors.",
	)
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

func infraSupportEmail() string {
	if supportEmail := strings.TrimSpace(os.Getenv("SUPPORT_EMAIL")); supportEmail != "" {
		return supportEmail
	}
	return "support@frameworks.network"
}

const infraAlertTemplate = `
{{if eq .Severity "CRITICAL"}}
<div style="background:#fff1f1; color:#7c3030; padding:13px 15px; border-left:3px solid #b74242; margin-bottom:20px; font-size:14px; line-height:21px;">
    <strong>Immediate attention required</strong>
</div>
{{else}}
<div style="background:#fff8e8; color:#6f4a16; padding:13px 15px; border-left:3px solid #a66b16; margin-bottom:20px; font-size:14px; line-height:21px;">
    <strong>Review this node soon</strong>
</div>
{{end}}

<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">An infrastructure issue was detected on your cluster.</p>

<table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin:20px 0; font-size:13px;">
    <tr style="background:#eef5f8;">
        <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Cluster</th>
        <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #ccdde5;"><strong>{{.ClusterName}}</strong> ({{.ClusterID}})</td>
    </tr>
    <tr>
        <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #e7edf0;">Node</th>
        <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;"><code>{{.NodeID}}</code></td>
    </tr>
</table>

<h2 style="color:#24283b; margin:28px 0 12px; font-size:18px; line-height:24px;">Issues detected</h2>
<table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin-bottom:20px; font-size:13px;">
    <tr style="background:#eef5f8;">
        <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Resource</th>
        <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Current</th>
        <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Threshold</th>
        <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Status</th>
    </tr>
    {{range .Alerts}}
    <tr>
        <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">{{alertLabel .}}</td>
        <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;"><strong>{{formatPercent .Current}}</strong></td>
        <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">{{formatPercent .Threshold}}</td>
        <td style="padding:9px 10px; border-bottom:1px solid #e7edf0; color:{{severityColor .}}; font-weight:bold;">{{.Severity}}</td>
    </tr>
    {{if hasBaseline .}}
    <tr style="background:#f7fafb;">
        <td colspan="4" style="padding:6px 10px; border-bottom:1px solid #e7edf0; color:#667085; font-size:11px; line-height:16px;">
            Baseline average: {{formatPercent .Baseline}}
        </td>
    </tr>
    {{end}}
    {{end}}
</table>

{{if .ActionItems}}
<h2 style="color:#24283b; margin:28px 0 12px; font-size:18px; line-height:24px;">What to do</h2>
<ul style="padding-left: 20px;">
    {{range .ActionItems}}
    <li style="margin-bottom:8px; color:#24283b; font-size:14px; line-height:21px;">{{.}}</li>
    {{end}}
</ul>
{{end}}

<p style="color:#667085; font-size:12px; line-height:18px; margin-top:28px;">
    Detected at {{.DetectedAt.Format "January 2, 2006 at 3:04 PM UTC"}}<br>
    This alert will not repeat for 4 hours.
</p>`
