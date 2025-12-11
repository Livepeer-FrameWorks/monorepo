package ansible

// Playbook represents an Ansible playbook structure
type Playbook struct {
	Name  string
	Hosts string
	Plays []Play
}

// Play represents a single play in a playbook
type Play struct {
	Name        string
	Hosts       string
	BecomeUser  string
	Become      bool
	GatherFacts bool
	Vars        map[string]interface{}
	Tasks       []Task
	Roles       []Role
	Handlers    []Handler
	PreTasks    []Task
	PostTasks   []Task
}

// Task represents an Ansible task
type Task struct {
	Name     string
	Module   string
	Args     map[string]interface{}
	When     string
	Register string
	Notify   []string
	Tags     []string
	Ignore   bool // ignore_errors
}

// Role represents an Ansible role
type Role struct {
	Name string
	Vars map[string]interface{}
}

// Handler represents an Ansible handler
type Handler struct {
	Name   string
	Module string
	Args   map[string]interface{}
}

// Inventory represents an Ansible inventory
type Inventory struct {
	Groups map[string]*InventoryGroup
	Hosts  map[string]*InventoryHost
}

// InventoryGroup represents a group in the inventory
type InventoryGroup struct {
	Name     string
	Hosts    []string
	Vars     map[string]string
	Children []string
}

// InventoryHost represents a host in the inventory
type InventoryHost struct {
	Name    string
	Address string
	Vars    map[string]string
}

// ExecuteOptions configures Ansible execution
type ExecuteOptions struct {
	Inventory  string // Path to inventory file
	Playbook   string // Path to playbook file
	ExtraVars  map[string]string
	Verbose    bool
	Check      bool // Dry-run mode
	Diff       bool
	Tags       []string
	SkipTags   []string
	Limit      string // Limit to specific hosts
	BecomeUser string
	PrivateKey string
	User       string
}

// ExecuteResult holds Ansible execution results
type ExecuteResult struct {
	Success     bool
	Output      string
	Error       error
	PlaybookRun *PlaybookRunStats
}

// PlaybookRunStats holds statistics from a playbook run
type PlaybookRunStats struct {
	Changed     int
	Failures    int
	Ok          int
	Skipped     int
	Unreachable int
}
