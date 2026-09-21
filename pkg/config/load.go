package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// RedactedValue replaces the value of a secret field in Describe output.
const RedactedValue = "[redacted]"

var durationType = reflect.TypeFor[time.Duration]()

type sourceKey struct {
	typ     reflect.Type
	address uintptr
}

type sourceSnapshot struct {
	key sourceKey
	set map[string]bool
}

// Metadata does not retain the configuration or any secret values.
var loadedSources sync.Map

func rememberSources[T any](cfg *T, set map[string]bool) {
	key := sourceKey{reflect.TypeOf(cfg), reflect.ValueOf(cfg).Pointer()}
	snapshot := &sourceSnapshot{key: key, set: set}
	loadedSources.Store(key, snapshot)
	runtime.AddCleanup(cfg, func(snapshot *sourceSnapshot) {
		loadedSources.CompareAndDelete(snapshot.key, snapshot)
	}, snapshot)
}

// Options controls how Load resolves configuration values.
type Options struct {
	// Service is the canonical pkg/servicedefs ID; it resolves the
	// @servicedefs port defaults.
	Service string
	// Lookup reads one raw value. Nil reads the process environment.
	Lookup func(key string) (string, bool)
	// Logger receives a warning when a deprecated key is set. Nil disables it.
	Logger logrus.FieldLogger
}

// value returns the trimmed raw value and whether it counts as set. A key that
// is present but blank is unset, so defaults and required checks apply.
func (o Options) value(key string) (string, bool) {
	lookup := o.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	raw, _ := lookup(key)
	raw = strings.TrimSpace(raw)
	return raw, raw != ""
}

// LoadError lists every missing and invalid key found in one pass, so an
// operator fixes the whole set instead of one fatal at a time. Invalid entries
// never include the rejected value, because it may be a secret.
type LoadError struct {
	Missing []string
	Invalid []string
}

func (e *LoadError) Error() string {
	parts := make([]string, 0, 2)
	if len(e.Missing) > 0 {
		parts = append(parts, "missing required "+strings.Join(e.Missing, ", "))
	}
	if len(e.Invalid) > 0 {
		parts = append(parts, "invalid "+strings.Join(e.Invalid, "; "))
	}
	return "config: " + strings.Join(parts, "; ")
}

// Keys lists the key names of every missing and invalid entry, sorted.
func (e *LoadError) Keys() []string {
	keys := append([]string(nil), e.Missing...)
	for _, entry := range e.Invalid {
		key, _, _ := strings.Cut(entry, " (")
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// ErrorKeys returns the configuration key names an error from Load names, or
// nil when it is not a LoadError.
func ErrorKeys(err error) []string {
	var loadErr *LoadError
	if errors.As(err, &loadErr) {
		return loadErr.Keys()
	}
	return nil
}

// Validator is implemented by configuration structs with cross-field rules.
// Load calls it after every field decoded successfully.
type Validator interface {
	Validate() error
}

// Load builds a T from its struct tags. Annotation errors (a malformed tag, an
// unsupported field type, a duplicate key) are programming errors and are
// returned before any value is read; value errors are collected into a
// LoadError.
func Load[T any](opts Options) (*T, error) {
	cfg := new(T)
	lookup := opts.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	set := make(map[string]bool)
	opts.Lookup = func(key string) (string, bool) {
		value, present := lookup(key)
		set[key] = strings.TrimSpace(value) != ""
		return value, present
	}
	if err := decode(cfg, opts); err != nil {
		return nil, err
	}
	rememberSources(cfg, set)
	return cfg, nil
}

func decode(target any, opts Options) error {
	ptr := reflect.ValueOf(target)
	if ptr.Kind() != reflect.Pointer || ptr.IsNil() || ptr.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: decode target must be a non-nil pointer to a struct, got %T", target)
	}
	if err := checkAnnotations(ptr.Elem().Type(), opts.Service); err != nil {
		return err
	}

	loadErr := &LoadError{}
	err := walkFields(ptr.Elem(), func(field reflect.Value, spec FieldSpec) error {
		raw, set := opts.value(spec.Env)
		switch {
		case set:
			if spec.Deprecated != "" && opts.Logger != nil {
				entry := opts.Logger.WithField("key", spec.Env).WithField("deprecated_since", spec.Deprecated)
				if spec.Replacement != "" {
					entry = entry.WithField("replacement", spec.Replacement)
				}
				entry.Warn("Deprecated configuration key is set")
			}
			if err := setField(field, raw); err != nil {
				loadErr.Invalid = append(loadErr.Invalid, fmt.Sprintf("%s (%v)", spec.Env, err))
			}
		case spec.HasDefault:
			def, err := ResolveDefault(spec, opts.Service)
			if err != nil {
				return err
			}
			if err := setField(field, def); err != nil {
				return fmt.Errorf("config: default for %s: %w", spec.Env, err)
			}
		case spec.Required:
			loadErr.Missing = append(loadErr.Missing, spec.Env)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(loadErr.Missing) > 0 || len(loadErr.Invalid) > 0 {
		return loadErr
	}
	if err := validateBlocks(ptr.Elem()); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if v, ok := target.(Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	return nil
}

// validateBlocks runs Validate on every nested configuration block, innermost
// first. A block's Validate is not promoted to the enclosing struct when that
// struct declares its own Validate or embeds two validating blocks, so the
// walk is what guarantees each block is checked.
func validateBlocks(v reflect.Value) error {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() || sf.Type.Kind() != reflect.Struct || sf.Type == durationType {
			continue
		}
		field := v.Field(i)
		if err := validateBlocks(field); err != nil {
			return err
		}
		if validator, ok := field.Addr().Interface().(Validator); ok {
			if err := validator.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkAnnotations validates every tag, field type, key uniqueness, and
// default before any value is read.
func checkAnnotations(t reflect.Type, service string) error {
	seen := map[string]bool{}
	return walkFields(reflect.New(t).Elem(), func(field reflect.Value, spec FieldSpec) error {
		if seen[spec.Env] {
			return fmt.Errorf("config: %s is declared more than once", spec.Env)
		}
		seen[spec.Env] = true
		if spec.HasDefault {
			def, err := ResolveDefault(spec, service)
			if err != nil {
				return err
			}
			probe := reflect.New(field.Type()).Elem()
			if err := setField(probe, def); err != nil {
				return fmt.Errorf("config: default for %s: %w", spec.Env, err)
			}
		}
		return nil
	})
}

// ResolveDefault returns the literal default for a field, resolving the
// @servicedefs port placeholders for the given service.
func ResolveDefault(spec FieldSpec, service string) (string, error) {
	switch spec.Default {
	case DefaultServiceHTTPPort:
		port, ok := servicedefs.DefaultPort(service)
		if !ok || port == 0 {
			return "", fmt.Errorf("config: %s defaults to the service HTTP port but service %q has none", spec.Env, service)
		}
		return strconv.Itoa(port), nil
	case DefaultServiceGRPCPort:
		port, ok := servicedefs.DefaultGRPCPort(service)
		if !ok {
			return "", fmt.Errorf("config: %s defaults to the service gRPC port but service %q has none", spec.Env, service)
		}
		return strconv.Itoa(port), nil
	default:
		return spec.Default, nil
	}
}

// walkFields visits every annotated field, descending into untagged struct
// fields such as embedded configuration blocks.
func walkFields(v reflect.Value, visit func(reflect.Value, FieldSpec) error) error {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			if sf.Anonymous {
				return fmt.Errorf("config: %s embeds unexported type %s; configuration blocks must be exported", t.Name(), sf.Type)
			}
			continue
		}
		spec, isConfig, err := ParseFieldTag(string(sf.Tag))
		if err != nil {
			return fmt.Errorf("config: %s.%s: %w", t.Name(), sf.Name, err)
		}
		field := v.Field(i)
		if !isConfig {
			if sf.Type.Kind() == reflect.Struct && sf.Type != durationType {
				if err := walkFields(field, visit); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("config: %s.%s has no env tag", t.Name(), sf.Name)
		}
		if !supportedType(sf.Type) {
			return fmt.Errorf("config: %s.%s has unsupported type %s", t.Name(), sf.Name, sf.Type)
		}
		if err := visit(field, spec); err != nil {
			return err
		}
	}
	return nil
}

func supportedType(t reflect.Type) bool {
	if t == durationType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Int:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.String
	default:
		return false
	}
}

func setField(field reflect.Value, raw string) error {
	if field.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("want a duration such as 30s")
		}
		field.SetInt(int64(d))
		return nil
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("want true or false")
		}
		field.SetBool(b)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("want an integer")
		}
		field.SetInt(int64(n))
	case reflect.Slice:
		field.Set(reflect.ValueOf(splitList(raw)).Convert(field.Type()))
	default:
		return fmt.Errorf("unsupported kind %s", field.Kind())
	}
	return nil
}

func splitList(raw string) []string {
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// FieldValue is one effective configuration value as reported by Describe.
type FieldValue struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Source     string `json:"source"` // env | default | unset
	Secret     bool   `json:"secret,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
}

// Describe reports the effective value and source of every field in a loaded
// configuration. Secret values are replaced with RedactedValue.
func Describe(target any, opts Options) ([]FieldValue, error) {
	if overlay, ok := target.(Overlay); ok {
		fields, err := Describe(overlay.Base, opts)
		if err != nil {
			return nil, err
		}
		for _, override := range overlay.Overrides {
			replacements, err := Describe(override, opts)
			if err != nil {
				return nil, err
			}
			for _, replacement := range replacements {
				for i := range fields {
					if fields[i].Key == replacement.Key {
						fields[i] = replacement
					}
				}
			}
		}
		return fields, nil
	}
	v := reflect.ValueOf(target)
	var sources *sourceSnapshot
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, fmt.Errorf("config: describe target is nil")
		}
		if snapshot, ok := loadedSources.Load(sourceKey{v.Type(), v.Pointer()}); ok {
			if typed, valid := snapshot.(*sourceSnapshot); valid {
				sources = typed
			}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config: describe target must be a struct, got %T", target)
	}
	var out []FieldValue
	err := walkFields(v, func(field reflect.Value, spec FieldSpec) error {
		entry := FieldValue{Key: spec.Env, Value: formatField(field), Secret: spec.Secret, Deprecated: spec.Deprecated}
		var set bool
		if sources != nil {
			set = sources.set[spec.Env]
		} else {
			_, set = opts.value(spec.Env)
		}
		switch {
		case set:
			entry.Source = "env"
		case spec.HasDefault:
			entry.Source = "default"
		default:
			entry.Source = "unset"
		}
		if spec.Secret && entry.Value != "" {
			entry.Value = RedactedValue
		}
		out = append(out, entry)
		return nil
	})
	runtime.KeepAlive(target)
	return out, err
}

// Overlay describes startup configuration with independently reloaded blocks,
// preserving the provenance of each snapshot as well as its effective values.
type Overlay struct {
	Base      any
	Overrides []any
}

func formatField(field reflect.Value) string {
	if field.Type() == durationType {
		return time.Duration(field.Int()).String()
	}
	switch field.Kind() {
	case reflect.String:
		return field.String()
	case reflect.Bool:
		return strconv.FormatBool(field.Bool())
	case reflect.Int:
		return strconv.FormatInt(field.Int(), 10)
	case reflect.Slice:
		parts := make([]string, field.Len())
		for i := range field.Len() {
			parts[i] = field.Index(i).String()
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

// Live holds a configuration that can be re-decoded after the process
// environment changes (SIGHUP env-file reload). Only fields read through Get
// at use time observe a reload; values copied out at startup do not.
type Live[T any] struct {
	current atomic.Pointer[T]
	opts    Options
}

// NewLive wraps an already-loaded configuration.
func NewLive[T any](initial *T, opts Options) *Live[T] {
	l := &Live[T]{opts: opts}
	if _, ok := loadedSources.Load(sourceKey{reflect.TypeOf(initial), reflect.ValueOf(initial).Pointer()}); !ok {
		copy := new(T)
		*copy = *initial
		set := map[string]bool{}
		if err := walkFields(reflect.ValueOf(copy).Elem(), func(_ reflect.Value, spec FieldSpec) error {
			_, set[spec.Env] = opts.value(spec.Env)
			return nil
		}); err != nil {
			panic(fmt.Sprintf("config: invalid live snapshot: %v", err))
		}
		rememberSources(copy, set)
		initial = copy
	}
	l.current.Store(initial)
	return l
}

// Get returns the current configuration snapshot.
func (l *Live[T]) Get() *T {
	return l.current.Load()
}

// Reload re-decodes from the environment. On error the previous snapshot stays
// in effect, so a bad env file never replaces a working configuration.
func (l *Live[T]) Reload() error {
	next, err := Load[T](l.opts)
	if err != nil {
		return err
	}
	l.current.Store(next)
	return nil
}
