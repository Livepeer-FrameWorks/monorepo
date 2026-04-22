package ansible

// DistroPackageSpec is the per-distro-family mapping for a logical dependency
// (e.g. "the Java runtime"). Ansible's ansible.builtin.package picks the
// package manager automatically, but does not translate names across distros
// — so the concrete package name still has to come from Go.
//
// ServiceName is the unit name systemd uses for the installed package (often
// differs from the package name, e.g. chrony/chronyd). Empty when no service
// ships with the package.
type DistroPackageSpec struct {
	PackageName string
	ServiceName string
}

// DistroPackageMap holds per-family specs. Missing families are treated as
// "not supported" — callers resolve with ResolveDistroPackage and handle the
// missing case explicitly.
type DistroPackageMap map[string]DistroPackageSpec

// ResolveDistroPackage returns the DistroPackageSpec for family, or the zero
// value and false if that family has no entry. family values match
// BaseProvisioner.DetectDistroFamily: "debian", "rhel", "arch", "alpine".
func ResolveDistroPackage(m DistroPackageMap, family string) (DistroPackageSpec, bool) {
	spec, ok := m[family]
	return spec, ok
}

// JavaRuntimePackages is the per-distro Java runtime (>= 11, headless).
var JavaRuntimePackages = DistroPackageMap{
	"debian": {PackageName: "default-jre-headless"},
	"rhel":   {PackageName: "java-17-openjdk-headless"},
	"arch":   {PackageName: "jre-openjdk-headless"},
}

// TimeSyncPackages is the per-distro chrony package. ServiceName differs:
// Debian's unit is chrony, RHEL/Arch's is chronyd.
var TimeSyncPackages = DistroPackageMap{
	"debian": {PackageName: "chrony", ServiceName: "chrony"},
	"rhel":   {PackageName: "chrony", ServiceName: "chronyd"},
	"arch":   {PackageName: "chrony", ServiceName: "chronyd"},
}

// CurlPackages is uniform across families but still lives here so callers
// route through the same lookup path.
var CurlPackages = DistroPackageMap{
	"debian": {PackageName: "curl"},
	"rhel":   {PackageName: "curl"},
	"arch":   {PackageName: "curl"},
	"alpine": {PackageName: "curl"},
}

// TimeSyncTasks returns the task set that ensures a time-sync daemon is
// running: gather service_facts, then install+start chrony only if no known
// time-sync service is already active. The "no timesync active" predicate
// covers chronyd, chrony, ntpd, ntp, and systemd-timesyncd.
func TimeSyncTasks(spec DistroPackageSpec) []Task {
	noTimesync := "" +
		"(ansible_facts.services['chronyd.service'] is not defined or ansible_facts.services['chronyd.service'].state != 'running') and " +
		"(ansible_facts.services['chrony.service'] is not defined or ansible_facts.services['chrony.service'].state != 'running') and " +
		"(ansible_facts.services['ntpd.service'] is not defined or ansible_facts.services['ntpd.service'].state != 'running') and " +
		"(ansible_facts.services['ntp.service'] is not defined or ansible_facts.services['ntp.service'].state != 'running') and " +
		"(ansible_facts.services['systemd-timesyncd.service'] is not defined or ansible_facts.services['systemd-timesyncd.service'].state != 'running')"

	tasks := []Task{{
		Name:   "gather service facts (timesync probe)",
		Module: "ansible.builtin.service_facts",
	}}
	pkg := TaskPackage(spec.PackageName, PackagePresent)
	pkg.When = noTimesync
	tasks = append(tasks, pkg)
	if spec.ServiceName != "" {
		svc := TaskSystemdService(spec.ServiceName, SystemdOpts{
			State:   "started",
			Enabled: BoolPtr(true),
			When:    noTimesync,
		})
		tasks = append(tasks, svc)
	}
	return tasks
}
