package heartbeat

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_consultant/internal/diagnostics"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/email"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

const (
	infraCooldownDuration  = 4 * time.Hour
	infraPersistenceWindow = 20 * time.Minute
	infraStaleThreshold    = 10 * time.Minute

	cpuThresholdPercent    = 95.0
	memoryThresholdPercent = 95.0
	diskWarningPercent     = 90.0
	diskCriticalPercent    = 95.0

	// Require sustained violation in 3 of 4 five-minute windows.
	persistenceWindows    = 4
	persistenceMinViolate = 3
)

type InfraAlertType string

const (
	InfraAlertCPU          InfraAlertType = "cpu_stuck"
	InfraAlertMemory       InfraAlertType = "memory_exhaustion"
	InfraAlertDiskWarning  InfraAlertType = "disk_warning"
	InfraAlertDiskCritical InfraAlertType = "disk_critical"
)

type InfraAlert struct {
	NodeID      string
	ClusterID   string
	ClusterName string
	AlertType   InfraAlertType
	Current     float64
	Threshold   float64
	Baseline    float64 // rolling average from BaselineEvaluator (0 if unavailable)
	DetectedAt  time.Time
}

func (a InfraAlert) Severity() string {
	switch a.AlertType {
	case InfraAlertCPU, InfraAlertDiskCritical, InfraAlertMemory:
		return "CRITICAL"
	default:
		return "WARNING"
	}
}

// InfraNodeClient provides Periscope methods needed by infra monitoring.
type InfraNodeClient interface {
	GetLiveNodes(ctx context.Context, tenantID string, nodeID *string, relatedTenantIDs []string) (*pb.GetLiveNodesResponse, error)
	GetNodePerformance5m(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*pb.GetNodePerformance5MResponse, error)
}

// InfraClusterClient provides Quartermaster methods for cluster/node discovery.
type InfraClusterClient interface {
	ListClusters(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListClustersResponse, error)
	GetNodeOwner(ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error)
}

type InfraMonitorConfig struct {
	Nodes     InfraNodeClient
	Clusters  InfraClusterClient
	Billing   BillingClient
	Baselines *diagnostics.BaselineEvaluator
	SMTP      email.Config
	Logger    logging.Logger
}

type InfraMonitor struct {
	nodes     InfraNodeClient
	clusters  InfraClusterClient
	billing   BillingClient
	baselines *diagnostics.BaselineEvaluator
	emailer   *email.Sender
	smtp      email.Config
	cooldown  *diagnostics.TriageCooldown
	logger    logging.Logger
}

func NewInfraMonitor(cfg *InfraMonitorConfig) *InfraMonitor {
	if cfg == nil || cfg.Nodes == nil || cfg.Clusters == nil {
		return nil
	}
	return &InfraMonitor{
		nodes:     cfg.Nodes,
		clusters:  cfg.Clusters,
		billing:   cfg.Billing,
		baselines: cfg.Baselines,
		emailer:   email.NewSender(cfg.SMTP),
		smtp:      cfg.SMTP,
		cooldown:  diagnostics.NewTriageCooldown(infraCooldownDuration),
		logger:    cfg.Logger,
	}
}

func (m *InfraMonitor) Run(ctx context.Context) {
	if m == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			m.logger.WithField("panic", fmt.Sprint(r)).Error("Infrastructure monitor panic")
		}
	}()

	clusters, err := m.discoverClusters(ctx)
	if err != nil {
		m.logger.WithError(err).Warn("Infra monitor: cluster discovery failed")
		return
	}

	seen := make(map[string]bool)
	for _, cluster := range clusters {
		ownerTenantID := cluster.GetOwnerTenantId()
		if ownerTenantID == "" {
			continue
		}
		if !cluster.GetIsActive() {
			continue
		}

		nodes, err := m.nodes.GetLiveNodes(ctx, ownerTenantID, nil, nil)
		if err != nil {
			m.logger.WithError(err).WithField("cluster_id", cluster.GetClusterId()).Warn("Infra monitor: live nodes fetch failed")
			continue
		}

		for _, node := range nodes.GetNodes() {
			if seen[node.GetNodeId()] {
				continue
			}
			seen[node.GetNodeId()] = true
			m.checkNode(ctx, node, cluster)
		}
	}
}

func (m *InfraMonitor) discoverClusters(ctx context.Context) ([]*pb.InfrastructureCluster, error) {
	var all []*pb.InfrastructureCluster
	var cursor string
	for {
		var pagination *pb.CursorPaginationRequest
		if cursor != "" {
			pagination = &pb.CursorPaginationRequest{First: 100, After: &cursor}
		} else {
			pagination = &pb.CursorPaginationRequest{First: 100}
		}
		resp, err := m.clusters.ListClusters(ctx, pagination)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.GetClusters()...)
		pg := resp.GetPagination()
		if pg == nil || !pg.GetHasNextPage() {
			break
		}
		next := pg.GetEndCursor()
		if next == "" {
			break
		}
		cursor = next
	}
	return all, nil
}

func (m *InfraMonitor) checkNode(ctx context.Context, node *pb.LiveNode, cluster *pb.InfrastructureCluster) {
	nodeID := node.GetNodeId()
	if nodeID == "" {
		return
	}

	if ts := node.GetUpdatedAt(); ts != nil {
		if time.Since(ts.AsTime()) > infraStaleThreshold {
			return
		}
	}

	clusterID := cluster.GetClusterId()
	clusterName := cluster.GetClusterName()
	ownerTenantID := cluster.GetOwnerTenantId()
	now := time.Now()

	cpuPercent := float64(node.GetCpuPercent())

	var memPercent float64
	if ramTotal := node.GetRamTotalBytes(); ramTotal > 0 {
		memPercent = float64(node.GetRamUsedBytes()) / float64(ramTotal) * 100
	}

	var diskPercent float64
	if diskTotal := node.GetDiskTotalBytes(); diskTotal > 0 {
		diskPercent = float64(node.GetDiskUsedBytes()) / float64(diskTotal) * 100
	}

	// Feed the baseline evaluator — same Welford running-average system used
	// for stream health. Key: (ownerTenantID, "node:"+nodeID) so each
	// physical node builds its own baseline. Deviation check runs BEFORE the
	// update so the current observation does not dilute the anomaly signal.
	metrics := map[string]float64{
		"node_cpu":    cpuPercent,
		"node_memory": memPercent,
		"node_disk":   diskPercent,
	}
	baselineKey := "node:" + nodeID

	var deviations []diagnostics.Deviation
	if m.baselines != nil {
		deviations, _ = m.baselines.Deviations(ctx, ownerTenantID, baselineKey, metrics)
		if err := m.baselines.Update(ctx, ownerTenantID, baselineKey, metrics); err != nil {
			m.logger.WithError(err).WithField("node_id", nodeID).Warn("Infra monitor: baseline update failed")
		}
	}

	// Resolve baseline averages for alert context.
	baselineCPU, baselineMemory, baselineDisk := m.resolveBaselines(deviations)

	var alerts []InfraAlert

	// CPU — hard threshold + persistence confirmation.
	if cpuPercent >= cpuThresholdPercent {
		if m.confirmPersistence(ctx, ownerTenantID, nodeID, "cpu") {
			alerts = append(alerts, InfraAlert{
				NodeID: nodeID, ClusterID: clusterID, ClusterName: clusterName,
				AlertType: InfraAlertCPU, Current: cpuPercent, Threshold: cpuThresholdPercent,
				Baseline: baselineCPU, DetectedAt: now,
			})
		}
	}

	// Memory — hard threshold + persistence confirmation.
	if memPercent >= memoryThresholdPercent {
		if m.confirmPersistence(ctx, ownerTenantID, nodeID, "memory") {
			alerts = append(alerts, InfraAlert{
				NodeID: nodeID, ClusterID: clusterID, ClusterName: clusterName,
				AlertType: InfraAlertMemory, Current: memPercent, Threshold: memoryThresholdPercent,
				Baseline: baselineMemory, DetectedAt: now,
			})
		}
	}

	// Disk — immediate (no persistence needed, disk doesn't self-heal).
	if diskPercent >= diskCriticalPercent {
		alerts = append(alerts, InfraAlert{
			NodeID: nodeID, ClusterID: clusterID, ClusterName: clusterName,
			AlertType: InfraAlertDiskCritical, Current: diskPercent, Threshold: diskCriticalPercent,
			Baseline: baselineDisk, DetectedAt: now,
		})
	} else if diskPercent >= diskWarningPercent {
		alerts = append(alerts, InfraAlert{
			NodeID: nodeID, ClusterID: clusterID, ClusterName: clusterName,
			AlertType: InfraAlertDiskWarning, Current: diskPercent, Threshold: diskWarningPercent,
			Baseline: baselineDisk, DetectedAt: now,
		})
	}

	// Log baseline deviations even when they don't cross hard thresholds.
	// This provides visibility into drift without sending email alerts.
	if len(deviations) > 0 && len(alerts) == 0 {
		for _, d := range deviations {
			m.logger.WithFields(logging.Fields{
				"node_id":    nodeID,
				"cluster_id": clusterID,
				"metric":     d.Metric,
				"current":    d.Current,
				"baseline":   d.Baseline,
				"sigma":      d.Sigma,
				"direction":  d.Direction,
			}).Info("INFRA_BASELINE_DEVIATION")
		}
	}

	for _, alert := range alerts {
		cooldownKey := fmt.Sprintf("infra:%s:%s", alert.NodeID, alert.AlertType)
		if !m.cooldown.ShouldFlag(cooldownKey) {
			continue
		}
		m.sendAlert(ctx, alert, ownerTenantID)
	}
}

func (m *InfraMonitor) resolveBaselines(deviations []diagnostics.Deviation) (cpu, memory, disk float64) {
	for _, d := range deviations {
		switch d.Metric {
		case "node_cpu":
			cpu = d.Baseline
		case "node_memory":
			memory = d.Baseline
		case "node_disk":
			disk = d.Baseline
		}
	}
	return
}

func (m *InfraMonitor) confirmPersistence(ctx context.Context, tenantID, nodeID, metric string) bool {
	now := time.Now()
	timeRange := &periscope.TimeRangeOpts{
		StartTime: now.Add(-infraPersistenceWindow),
		EndTime:   now,
	}
	resp, err := m.nodes.GetNodePerformance5m(ctx, tenantID, &nodeID, timeRange, nil)
	if err != nil {
		m.logger.WithError(err).WithField("node_id", nodeID).Warn("Infra monitor: persistence check failed")
		return false
	}
	records := resp.GetRecords()
	if len(records) == 0 {
		return false
	}

	violations := 0
	for _, rec := range records {
		var val float64
		switch metric {
		case "cpu":
			val = float64(rec.GetAvgCpu())
		case "memory":
			val = float64(rec.GetAvgMemory())
		default:
			return false
		}
		threshold := cpuThresholdPercent
		if metric == "memory" {
			threshold = memoryThresholdPercent
		}
		if val >= threshold {
			violations++
		}
	}
	minWindows := persistenceMinViolate
	if len(records) < persistenceWindows {
		minWindows = len(records)
	}
	return violations >= minWindows
}

func (m *InfraMonitor) sendAlert(ctx context.Context, alert InfraAlert, ownerTenantID string) {
	recipientEmail := m.resolveOwnerEmail(ctx, alert.NodeID, ownerTenantID)
	if recipientEmail == "" {
		m.logger.WithField("node_id", alert.NodeID).Warn("Infra monitor: no owner email found, skipping alert")
		return
	}

	subject := fmt.Sprintf("[FrameWorks] Infrastructure Alert: %s on %s/%s",
		alert.Severity(), alert.ClusterName, alert.NodeID)

	body, err := renderInfraAlertEmail([]InfraAlert{alert})
	if err != nil {
		m.logger.WithError(err).WithField("node_id", alert.NodeID).Warn("Infra monitor: email render failed")
		return
	}

	if m.smtp.Host == "" || m.smtp.From == "" {
		m.logger.WithField("node_id", alert.NodeID).Warn("Infra monitor: SMTP not configured, skipping email")
		return
	}

	if err := m.emailer.SendMail(ctx, recipientEmail, subject, body); err != nil {
		m.logger.WithError(err).WithFields(logging.Fields{
			"to":      recipientEmail,
			"node_id": alert.NodeID,
		}).Error("Infra monitor: failed to send alert email")
		return
	}
	m.logger.WithFields(logging.Fields{
		"to":         recipientEmail,
		"node_id":    alert.NodeID,
		"alert_type": string(alert.AlertType),
		"severity":   alert.Severity(),
	}).Info("Infrastructure alert email sent")
}

func (m *InfraMonitor) resolveOwnerEmail(ctx context.Context, nodeID, fallbackTenantID string) string {
	ownerTenantID := fallbackTenantID
	if m.clusters != nil {
		owner, err := m.clusters.GetNodeOwner(ctx, nodeID)
		if err == nil && owner.GetOwnerTenantId() != "" {
			ownerTenantID = owner.GetOwnerTenantId()
		}
	}
	if ownerTenantID == "" || m.billing == nil {
		return ""
	}
	status, err := m.billing.GetBillingStatus(ctx, ownerTenantID)
	if err != nil {
		m.logger.WithError(err).WithField("tenant_id", ownerTenantID).Warn("Infra monitor: billing status lookup failed")
		return ""
	}
	sub := status.GetSubscription()
	if sub == nil {
		return ""
	}
	return sub.GetBillingEmail()
}
