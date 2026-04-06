package provisioner

import (
	"reflect"
	"testing"
)

func TestInitialPKIPaths(t *testing.T) {
	got := initialPKIPaths()
	want := []string{"/etc/frameworks/pki/ca.crt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initialPKIPaths() = %v, want %v", got, want)
	}
}
