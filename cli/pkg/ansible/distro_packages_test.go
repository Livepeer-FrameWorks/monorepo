package ansible

import "testing"

func TestResolveDistroPackage_coversAllThreeLinuxFamilies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    DistroPackageMap
		family  string
		wantPkg string
		wantSvc string
	}{
		{"java/debian", JavaRuntimePackages, "debian", "default-jre-headless", ""},
		{"java/rhel", JavaRuntimePackages, "rhel", "java-17-openjdk-headless", ""},
		{"java/arch", JavaRuntimePackages, "arch", "jre-openjdk-headless", ""},
		{"timesync/debian", TimeSyncPackages, "debian", "chrony", "chrony"},
		{"timesync/rhel", TimeSyncPackages, "rhel", "chrony", "chronyd"},
		{"timesync/arch", TimeSyncPackages, "arch", "chrony", "chronyd"},
		{"curl/debian", CurlPackages, "debian", "curl", ""},
		{"curl/rhel", CurlPackages, "rhel", "curl", ""},
		{"curl/arch", CurlPackages, "arch", "curl", ""},
		{"curl/alpine", CurlPackages, "alpine", "curl", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, ok := ResolveDistroPackage(tc.spec, tc.family)
			if !ok {
				t.Fatalf("family %q not resolved", tc.family)
			}
			if spec.PackageName != tc.wantPkg {
				t.Errorf("PackageName: got %q want %q", spec.PackageName, tc.wantPkg)
			}
			if spec.ServiceName != tc.wantSvc {
				t.Errorf("ServiceName: got %q want %q", spec.ServiceName, tc.wantSvc)
			}
		})
	}
}

func TestResolveDistroPackage_missingFamilyReportsNotFound(t *testing.T) {
	t.Parallel()
	if _, ok := ResolveDistroPackage(JavaRuntimePackages, "alpine"); ok {
		t.Error("JavaRuntimePackages should not have alpine entry")
	}
	if _, ok := ResolveDistroPackage(TimeSyncPackages, "unknown"); ok {
		t.Error("unknown family should not resolve")
	}
}

func TestJavaRuntimeTasks_probeAndConditionalPackageInstall(t *testing.T) {
	t.Parallel()

	tasks := JavaRuntimeTasks(DistroPackageSpec{PackageName: "jre-openjdk-headless"})
	if len(tasks) != 2 {
		t.Fatalf("JavaRuntimeTasks() len = %d, want 2", len(tasks))
	}

	probe := tasks[0]
	if probe.Name != "probe java runtime" {
		t.Fatalf("probe.Name = %q, want %q", probe.Name, "probe java runtime")
	}
	if probe.Module != "ansible.builtin.shell" {
		t.Fatalf("probe.Module = %q, want ansible.builtin.shell", probe.Module)
	}
	if probe.Register != "frameworks_java_runtime_probe" {
		t.Fatalf("probe.Register = %q, want frameworks_java_runtime_probe", probe.Register)
	}
	if !probe.Ignore {
		t.Fatal("probe must ignore errors so missing/incompatible java falls through to package install")
	}
	if probe.ChangedWhen != "false" {
		t.Fatalf("probe.ChangedWhen = %q, want false", probe.ChangedWhen)
	}

	pkg := tasks[1]
	if pkg.Module != "ansible.builtin.package" {
		t.Fatalf("pkg.Module = %q, want ansible.builtin.package", pkg.Module)
	}
	if got := pkg.Args["name"]; got != "jre-openjdk-headless" {
		t.Fatalf("pkg.Args[name] = %#v, want jre-openjdk-headless", got)
	}
	if pkg.When != "frameworks_java_runtime_probe.rc != 0" {
		t.Fatalf("pkg.When = %q, want probe failure gate", pkg.When)
	}
}
