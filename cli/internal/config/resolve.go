package config

import (
	"fmt"
	"sort"
)

type Env interface {
	Get(key string) string
}

type OSEnv struct{}

func (OSEnv) Get(key string) string { return osGetenv(key) }

type MapEnv map[string]string

func (m MapEnv) Get(key string) string { return m[key] }

func ResolveActiveContext(o RuntimeOverrides, env Env, cfg Config) (Context, error) {
	name, source, haveOpinion := lookupContextName(o, env, cfg)
	if !haveOpinion {
		return Context{}, fmt.Errorf("no current context configured: run 'frameworks setup'")
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return Context{}, unknownContextError(name, source, cfg)
	}
	ctx.Name = name
	return ctx, nil
}

// MaybeActiveContext returns a zero Context when no source at all names
// one; still errors when any source (flag/env/current) names a context
// that doesn't exist. The distinction matters: --context wrongname must
// fail loudly, but a fresh machine with no config and explicit manifest
// flags must work.
func MaybeActiveContext(o RuntimeOverrides, env Env, cfg Config) (Context, error) {
	name, source, haveOpinion := lookupContextName(o, env, cfg)
	if !haveOpinion {
		return Context{}, nil
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return Context{}, unknownContextError(name, source, cfg)
	}
	ctx.Name = name
	return ctx, nil
}

func lookupContextName(o RuntimeOverrides, env Env, cfg Config) (name, source string, haveOpinion bool) {
	if o.ContextExplicit && o.ContextName != "" {
		return o.ContextName, "--context flag", true
	}
	if v := env.Get("FRAMEWORKS_CONTEXT"); v != "" {
		return v, "FRAMEWORKS_CONTEXT env", true
	}
	if cfg.Current != "" {
		return cfg.Current, "config 'current'", true
	}
	return "", "", false
}

func unknownContextError(name, source string, cfg Config) error {
	available := make([]string, 0, len(cfg.Contexts))
	for n := range cfg.Contexts {
		available = append(available, n)
	}
	sort.Strings(available)
	if len(available) == 0 {
		return fmt.Errorf("context %q (from %s) not found; no contexts configured. Run 'frameworks setup' or 'frameworks context create %s'", name, source, name)
	}
	return fmt.Errorf("context %q (from %s) not found. Available: %v", name, source, available)
}
