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
	if !catalogFlagSensitive(entry, "enrollment-token") {
		t.Fatalf("edge provision enrollment-token flag should be sensitive")
	}

	contextCheck, ok := byCommand["frameworks context check"]
	if !ok {
		t.Fatalf("catalog missing context check")
	}
	if contextCheck.Risk != "" {
		t.Fatalf("context check risk = %q, want empty", contextCheck.Risk)
	}

	setup, ok := byCommand["frameworks setup"]
	if !ok {
		t.Fatalf("catalog missing setup")
	}
	if !setup.Interactive {
		t.Fatalf("setup should be marked interactive")
	}

	adminCreate, ok := byCommand["frameworks admin clusters create"]
	if !ok {
		t.Fatalf("catalog missing admin clusters create")
	}
	if !catalogFlagRequired(adminCreate, "cluster-id") {
		t.Fatalf("admin clusters create cluster-id flag should be required")
	}

	servicesDown, ok := byCommand["frameworks services down"]
	if !ok {
		t.Fatalf("catalog missing services down")
	}
	if !catalogFlagConfirmation(servicesDown, "yes") {
		t.Fatalf("services down yes flag should be marked as confirmation")
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
				if flag.Source != "frameworks" {
					t.Fatalf("context flag source = %q, want frameworks", flag.Source)
				}
				return
			}
		}
		t.Fatalf("context check missing inherited --context flag")
	}
	t.Fatalf("catalog missing context check")
}

func TestBuildCommandCatalogIncludesArguments(t *testing.T) {
	t.Parallel()

	catalog := buildCommandCatalog(NewRootCmd(), false)
	for _, entry := range catalog.Commands {
		if entry.Command != "frameworks admin bootstrap-tokens revoke" {
			continue
		}
		if len(entry.Arguments) != 1 {
			t.Fatalf("bootstrap token revoke arguments = %d, want 1", len(entry.Arguments))
		}
		arg := entry.Arguments[0]
		if arg.Name != "id" || arg.Required || arg.Raw != "[id]" {
			t.Fatalf("bootstrap token revoke argument = %+v, want optional id", arg)
		}
		return
	}
	t.Fatalf("catalog missing bootstrap token revoke")
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

func catalogFlagSensitive(entry commandCatalogEntry, name string) bool {
	for _, flag := range entry.Flags {
		if flag.Name == name {
			return flag.Sensitive
		}
	}
	return false
}

func catalogFlagConfirmation(entry commandCatalogEntry, name string) bool {
	for _, flag := range entry.Flags {
		if flag.Name == name {
			return flag.Confirmation
		}
	}
	return false
}
