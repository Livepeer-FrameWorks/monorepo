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
