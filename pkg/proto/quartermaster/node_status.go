package quartermasterpb

// Node statuses are shared by lifecycle mutations and processing inventory.
const (
	NodeStatusActive      = "active"
	NodeStatusOffline     = "offline"
	NodeStatusMaintenance = "maintenance"
	NodeStatusRetired     = "retired"
	NodeStatusEvicted     = "evicted"
)
