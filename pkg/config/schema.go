package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Struct tag keys that annotate a typed configuration field. Load and Describe
// read them through reflection at runtime; scripts/configref reads the same
// keys from source to generate the operator configuration reference, so both
// paths validate through ParseFieldTag.
const (
	TagEnv         = "env"
	TagDesc        = "desc"
	TagIntroduced  = "introduced"
	TagDefault     = "default"
	TagRequired    = "required"
	TagSecret      = "secret"
	TagDeprecated  = "deprecated"
	TagReplacement = "replacement"
)

// Default placeholders that resolve to the service's canonical port in
// pkg/servicedefs, so a port number is declared in exactly one place.
const (
	DefaultServiceHTTPPort = "@servicedefs.http_port"
	DefaultServiceGRPCPort = "@servicedefs.grpc_port"
)

const minDescriptionLength = 10

var (
	envNamePattern        = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	releaseVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	knownTagKeys          = map[string]bool{
		TagEnv: true, TagDesc: true, TagIntroduced: true, TagDefault: true,
		TagRequired: true, TagSecret: true, TagDeprecated: true, TagReplacement: true,
	}
)

// FieldSpec is the validated annotation of one configuration field.
type FieldSpec struct {
	Env         string
	Desc        string
	Introduced  string
	Default     string
	HasDefault  bool
	Required    bool
	Secret      bool
	Deprecated  string
	Replacement string
}

// ParseFieldTag validates the configuration annotations in a raw struct tag.
// The boolean result is false when the tag has no env key, which marks a field
// that is not a configuration value (for example an embedded block).
func ParseFieldTag(tag string) (FieldSpec, bool, error) {
	pairs, err := parseTagPairs(tag)
	if err != nil {
		return FieldSpec{}, false, err
	}
	env, hasEnv := pairs[TagEnv]
	if !hasEnv {
		for key := range pairs {
			if knownTagKeys[key] {
				return FieldSpec{}, false, fmt.Errorf("tag key %q requires an env key", key)
			}
		}
		return FieldSpec{}, false, nil
	}
	for key := range pairs {
		if !knownTagKeys[key] {
			return FieldSpec{}, true, fmt.Errorf("env %s: unknown tag key %q", env, key)
		}
	}

	spec := FieldSpec{
		Env:         env,
		Desc:        strings.TrimSpace(pairs[TagDesc]),
		Introduced:  pairs[TagIntroduced],
		Deprecated:  pairs[TagDeprecated],
		Replacement: pairs[TagReplacement],
	}
	spec.Default, spec.HasDefault = pairs[TagDefault]

	if !envNamePattern.MatchString(spec.Env) {
		return spec, true, fmt.Errorf("env %q must be upper snake case", spec.Env)
	}
	if len(spec.Desc) < minDescriptionLength {
		return spec, true, fmt.Errorf("env %s: desc must be at least %d characters", spec.Env, minDescriptionLength)
	}
	if !releaseVersionPattern.MatchString(spec.Introduced) {
		return spec, true, fmt.Errorf("env %s: introduced %q must be a release version vX.Y.Z", spec.Env, spec.Introduced)
	}
	if spec.Required, err = parseTrueFlag(pairs, TagRequired); err != nil {
		return spec, true, fmt.Errorf("env %s: %w", spec.Env, err)
	}
	if spec.Secret, err = parseTrueFlag(pairs, TagSecret); err != nil {
		return spec, true, fmt.Errorf("env %s: %w", spec.Env, err)
	}
	if spec.Required && spec.HasDefault {
		return spec, true, fmt.Errorf("env %s: a field cannot be both required and defaulted", spec.Env)
	}
	if spec.Deprecated != "" && !releaseVersionPattern.MatchString(spec.Deprecated) {
		return spec, true, fmt.Errorf("env %s: deprecated %q must be a release version vX.Y.Z", spec.Env, spec.Deprecated)
	}
	if spec.Replacement != "" {
		if spec.Deprecated == "" {
			return spec, true, fmt.Errorf("env %s: replacement requires deprecated", spec.Env)
		}
		if !envNamePattern.MatchString(spec.Replacement) {
			return spec, true, fmt.Errorf("env %s: replacement %q must be upper snake case", spec.Env, spec.Replacement)
		}
	}
	return spec, true, nil
}

// IsReleaseVersion reports whether v is a plain vX.Y.Z release version, the
// only form accepted by introduced and deprecated annotations.
func IsReleaseVersion(v string) bool {
	return releaseVersionPattern.MatchString(v)
}

func parseTrueFlag(pairs map[string]string, key string) (bool, error) {
	value, ok := pairs[key]
	if !ok {
		return false, nil
	}
	if value != "true" {
		return false, fmt.Errorf("tag %s must be \"true\" when present, got %q", key, value)
	}
	return true, nil
}

// parseTagPairs splits a struct tag into key/value pairs using the same
// grammar as reflect.StructTag, but reports malformed input and duplicate keys
// instead of silently ignoring them.
func parseTagPairs(tag string) (map[string]string, error) {
	pairs := map[string]string{}
	for {
		tag = strings.TrimLeft(tag, " ")
		if tag == "" {
			return pairs, nil
		}
		i := 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return nil, fmt.Errorf("malformed struct tag near %q", tag)
		}
		name := tag[:i]
		tag = tag[i+1:]

		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return nil, fmt.Errorf("unterminated value for struct tag key %q", name)
		}
		value, err := strconv.Unquote(tag[:i+1])
		if err != nil {
			return nil, fmt.Errorf("malformed value for struct tag key %q: %w", name, err)
		}
		tag = tag[i+1:]
		if _, dup := pairs[name]; dup {
			return nil, fmt.Errorf("duplicate struct tag key %q", name)
		}
		pairs[name] = value
	}
}
