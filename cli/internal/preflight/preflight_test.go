package preflight

import "testing"

func TestDiskSpaceFromDF(t *testing.T) {
	output := `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/sda1 1000000 100000 900000 10% /`

	check := DiskSpaceFromDF(output, "/", 100*1024*1024, 5)
	if !check.OK {
		t.Fatalf("expected disk check to pass, got %#v", check)
	}
}

func TestDiskSpaceFromDFFailure(t *testing.T) {
	output := `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/sda1 1000000 950000 50000 95% /`

	check := DiskSpaceFromDF(output, "/", 200*1024*1024, 10)
	if check.OK {
		t.Fatalf("expected disk check to fail, got %#v", check)
	}
}

func TestDiskSpaceFromDFInvalid(t *testing.T) {
	check := DiskSpaceFromDF("nope", "/", 0, 0)
	if check.OK {
		t.Fatalf("expected invalid df output to fail, got %#v", check)
	}
	if check.Error == "" {
		t.Fatalf("expected error to be set for invalid df output")
	}
}
