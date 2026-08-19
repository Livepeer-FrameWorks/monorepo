package cmd

import (
	"testing"
)

func TestClusterStorageDescriptorIsDiscoverable(t *testing.T) {
	storage := newClusterStorageCmd()
	command, _, err := storage.Find([]string{"descriptor"})
	if err != nil {
		t.Fatal(err)
	}
	if command == storage || command.Name() != "descriptor" {
		t.Fatalf("storage descriptor command not registered: got %q", command.Name())
	}
	if command.Hidden {
		t.Fatal("storage descriptor must remain visible after removing the adoption workflow")
	}
}
