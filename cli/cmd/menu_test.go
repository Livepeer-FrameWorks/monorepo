package cmd

import (
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

func TestMenuSectionsForPersona_userRecommendsAccountSection(t *testing.T) {
	t.Parallel()
	sections := menuSectionsForPersona(fwcfg.PersonaUser)
	if len(sections) == 0 {
		t.Fatal("expected sections")
	}
	if sections[0].key != "account" {
		t.Errorf("user persona should lead with account section, got %q", sections[0].key)
	}
	if !sections[0].recommended {
		t.Errorf("account section should be tagged Recommended for user persona")
	}
}

func TestMenuSectionsForPersona_selfHostedRecommendsEdgeSection(t *testing.T) {
	t.Parallel()
	sections := menuSectionsForPersona(fwcfg.PersonaSelfHosted)
	if len(sections) == 0 {
		t.Fatal("expected sections")
	}
	if sections[0].key != "edge" {
		t.Errorf("selfhosted persona should lead with edge section, got %q", sections[0].key)
	}
	if !sections[0].recommended {
		t.Errorf("edge section should be tagged Recommended for selfhosted persona")
	}
}

func TestMenuSectionsForPersona_platformRecommendsClusterAndControlPlane(t *testing.T) {
	t.Parallel()
	sections := menuSectionsForPersona(fwcfg.PersonaPlatform)
	recommended := map[string]bool{}
	for _, s := range sections {
		if s.recommended {
			recommended[s.key] = true
		}
	}
	if !recommended["cluster"] {
		t.Errorf("platform persona should tag cluster as Recommended, got %v", recommended)
	}
	if !recommended["control-plane"] {
		t.Errorf("platform persona should tag control-plane as Recommended, got %v", recommended)
	}
}

func TestMenuSectionsForPersona_filtersPlatformOnlySections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		persona fwcfg.Persona
		want    []string
	}{
		{fwcfg.PersonaUser, []string{"account", "settings"}},
		{fwcfg.PersonaEdge, []string{"account", "settings"}},
		{fwcfg.PersonaSelfHosted, []string{"edge", "account", "settings"}},
		{fwcfg.PersonaPlatform, []string{"cluster", "control-plane", "services", "dns-mesh", "edge", "account", "settings"}},
		{"", []string{"account", "settings"}},
	}
	for _, c := range cases {
		sections := menuSectionsForPersona(c.persona)
		if len(sections) != len(c.want) {
			t.Fatalf("persona %q: expected %d sections, got %d", c.persona, len(c.want), len(sections))
		}
		for i, section := range sections {
			if section.key != c.want[i] {
				t.Errorf("persona %q section %d = %q, want %q", c.persona, i, section.key, c.want[i])
			}
		}
	}
}

func TestSetupNextSteps_byPersona(t *testing.T) {
	t.Parallel()
	cases := []struct {
		persona   fwcfg.Persona
		wantFirst string
	}{
		{fwcfg.PersonaUser, "frameworks login"},
		{fwcfg.PersonaPlatform, "frameworks context check"},
		{fwcfg.PersonaSelfHosted, "frameworks login"},
	}
	for _, c := range cases {
		steps := setupNextSteps(c.persona)
		if len(steps) == 0 {
			t.Errorf("persona %q: got no next steps", c.persona)
			continue
		}
		if steps[0].Cmd != c.wantFirst {
			t.Errorf("persona %q: first next-step = %q, want %q", c.persona, steps[0].Cmd, c.wantFirst)
		}
	}
}

func TestLoginNextSteps_userPointsAtMenu(t *testing.T) {
	t.Parallel()
	steps := loginNextSteps(fwcfg.PersonaUser)
	if len(steps) == 0 {
		t.Fatal("expected at least one next step")
	}
	if steps[0].Cmd != "frameworks menu" {
		t.Errorf("user persona: first step should be menu, got %q", steps[0].Cmd)
	}
}

func TestLoginNextSteps_platformPointsAtClusterProvision(t *testing.T) {
	t.Parallel()
	steps := loginNextSteps(fwcfg.PersonaPlatform)
	if len(steps) == 0 {
		t.Fatal("expected at least one next step")
	}
	if steps[0].Cmd[:len("frameworks cluster provision")] != "frameworks cluster provision" {
		t.Errorf("platform persona: first step should be cluster provision, got %q", steps[0].Cmd)
	}
}

func TestLoginNextSteps_noContextFallsBackToSetup(t *testing.T) {
	t.Parallel()
	steps := loginNextSteps("")
	if len(steps) == 0 {
		t.Fatal("expected fallback next step")
	}
	if steps[0].Cmd != "frameworks setup" {
		t.Errorf("no-context fallback should suggest setup, got %q", steps[0].Cmd)
	}
}
