package cmd

import "testing"

func TestBuildCommandCatalogIncludesRunnableCommandsAndFlags(t *testing.T) {
	t.Parallel()

	catalog := buildCommandCatalog(NewRootCmd(), false)
	byCommand := map[string]commandCatalogEntry{}
	for _, entry := range catalog.Commands {
		byCommand[entry.Command] = entry
	}

	entry, ok := byCommand["frameworks edge provision"]
	if !ok {
		t.Fatalf("catalog missing edge provision")
	}
	if !entry.Runnable {
		t.Fatalf("edge provision should be runnable")
	}
	if entry.Risk != "mutating" {
		t.Fatalf("edge provision risk = %q, want mutating", entry.Risk)
	}
	if !hasCatalogFlag(entry, "enrollment-token") {
		t.Fatalf("edge provision missing enrollment-token flag")
	}
	contextCheck, ok := byCommand["frameworks context check"]
	if !ok {
		t.Fatalf("catalog missing context check")
	}
	if contextCheck.Risk != "" {
		t.Fatalf("context check risk = %q, want empty", contextCheck.Risk)
	}

	adminCreate, ok := byCommand["frameworks admin clusters create"]
	if !ok {
		t.Fatalf("catalog missing admin clusters create")
	}
	if !catalogFlagRequired(adminCreate, "cluster-id") {
		t.Fatalf("admin clusters create cluster-id flag should be required")
	}
}

func TestBuildCommandCatalogIncludesRootPersistentFlags(t *testing.T) {
	t.Parallel()

	catalog := buildCommandCatalog(NewRootCmd(), false)
	for _, entry := range catalog.Commands {
		if entry.Command != "frameworks context check" {
			continue
		}
		for _, flag := range entry.Flags {
			if flag.Name == "context" && flag.Scope == "inherited" {
				return
			}
		}
		t.Fatalf("context check missing inherited --context flag")
	}
	t.Fatalf("catalog missing context check")
}

func hasCatalogFlag(entry commandCatalogEntry, name string) bool {
	for _, flag := range entry.Flags {
		if flag.Name == name {
			return true
		}
	}
	return false
}

func catalogFlagRequired(entry commandCatalogEntry, name string) bool {
	for _, flag := range entry.Flags {
		if flag.Name == name {
			return flag.Required
		}
	}
	return false
}
