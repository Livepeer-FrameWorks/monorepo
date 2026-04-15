package provisioner

import (
	"reflect"
	"testing"
)

func TestInitialPKIPaths_NoServices(t *testing.T) {
	got := initialPKIPaths(nil)
	want := []string{"/etc/frameworks/pki/ca.crt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initialPKIPaths(nil) = %v, want %v", got, want)
	}
}

func TestInitialPKIPaths_WithServices(t *testing.T) {
	got := initialPKIPaths([]string{"commodore", "quartermaster"})
	want := []string{
		"/etc/frameworks/pki/ca.crt",
		"/etc/frameworks/pki/services/commodore/tls.crt",
		"/etc/frameworks/pki/services/commodore/tls.key",
		"/etc/frameworks/pki/services/quartermaster/tls.crt",
		"/etc/frameworks/pki/services/quartermaster/tls.key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initialPKIPaths(services) = %v, want %v", got, want)
	}
}
