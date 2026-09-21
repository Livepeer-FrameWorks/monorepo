// Package events is the domain event registry and envelope. Every event type
// is a protobuf message in pkg/proto/events/public/v1 or
// pkg/proto/events/internal/v1 annotated with frameworks.events.event; the
// registry is built from those descriptors, so the type string, visibility,
// tenant scope, and aggregate have one source.
package events

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	_ "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	_ "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Proto packages that hold event messages, with the visibility every event in
// them must declare.
const (
	PublicPackage   protoreflect.FullName = "frameworks.events.public.v1"
	InternalPackage protoreflect.FullName = "frameworks.events.internal.v1"
)

var (
	typePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	aggregatePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Spec is one registered event type.
type Spec struct {
	Type       string
	Visibility eventspb.Visibility
	Scope      eventspb.Scope
	Aggregate  string
	// MessageName is the full protobuf name, used as ce_dataschema.
	MessageName protoreflect.FullName

	messageType protoreflect.MessageType
}

// Public reports whether tenants may receive the event.
func (s Spec) Public() bool { return s.Visibility == eventspb.Visibility_VISIBILITY_PUBLIC }

// NewMessage returns an empty message of the event's registered type.
func (s Spec) NewMessage() proto.Message { return s.messageType.New().Interface() }

// Registry maps event types and message names to their specs.
type Registry struct {
	byType    map[string]Spec
	byMessage map[protoreflect.FullName]Spec
}

// Lookup returns the spec registered for eventType.
func (r *Registry) Lookup(eventType string) (Spec, bool) {
	spec, ok := r.byType[eventType]
	return spec, ok
}

// SpecFor returns the spec of msg's type.
func (r *Registry) SpecFor(msg proto.Message) (Spec, bool) {
	if msg == nil {
		return Spec{}, false
	}
	spec, ok := r.byMessage[msg.ProtoReflect().Descriptor().FullName()]
	return spec, ok
}

// Specs returns every registered spec ordered by type.
func (r *Registry) Specs() []Spec {
	out := make([]Spec, 0, len(r.byType))
	for _, spec := range r.byType {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// buildRegistry reads every top-level message annotated with
// frameworks.events.event in files. Messages without the annotation are
// submessages such as Artifact and Money. packages maps each event package to
// the visibility its events must declare; an annotated message in any other
// package is an error.
func buildRegistry(files []protoreflect.FileDescriptor, packages map[protoreflect.FullName]eventspb.Visibility) (*Registry, error) {
	reg := &Registry{byType: map[string]Spec{}, byMessage: map[protoreflect.FullName]Spec{}}
	var errs []error
	for _, fd := range files {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			opts := md.Options()
			if opts == nil || !proto.HasExtension(opts, eventspb.E_Event) {
				continue
			}
			ann, ok := proto.GetExtension(opts, eventspb.E_Event).(*eventspb.EventSpec)
			if !ok || ann == nil {
				continue
			}
			name := md.FullName()
			wantVisibility, known := packages[fd.Package()]
			switch {
			case !known:
				errs = append(errs, fmt.Errorf("%s: event message outside an event package", name))
				continue
			case !typePattern.MatchString(ann.GetType()):
				errs = append(errs, fmt.Errorf("%s: event type %q must be dotted lower snake case", name, ann.GetType()))
				continue
			case ann.GetVisibility() != wantVisibility:
				errs = append(errs, fmt.Errorf("%s: visibility %s, package %s requires %s", name, ann.GetVisibility(), fd.Package(), wantVisibility))
				continue
			case ann.GetScope() != eventspb.Scope_SCOPE_TENANT && ann.GetScope() != eventspb.Scope_SCOPE_PLATFORM:
				errs = append(errs, fmt.Errorf("%s: scope must be TENANT or PLATFORM", name))
				continue
			case !aggregatePattern.MatchString(ann.GetAggregate()):
				errs = append(errs, fmt.Errorf("%s: aggregate %q must be lower snake case", name, ann.GetAggregate()))
				continue
			}
			if prev, dup := reg.byType[ann.GetType()]; dup {
				errs = append(errs, fmt.Errorf("event type %q declared by both %s and %s", ann.GetType(), prev.MessageName, name))
				continue
			}
			spec := Spec{
				Type:        ann.GetType(),
				Visibility:  ann.GetVisibility(),
				Scope:       ann.GetScope(),
				Aggregate:   ann.GetAggregate(),
				MessageName: name,
				messageType: messageTypeFor(md),
			}
			reg.byType[spec.Type] = spec
			reg.byMessage[name] = spec
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return reg, nil
}

func messageTypeFor(md protoreflect.MessageDescriptor) protoreflect.MessageType {
	if mt, err := protoregistry.GlobalTypes.FindMessageByName(md.FullName()); err == nil && mt.Descriptor() == md {
		return mt
	}
	return dynamicpb.NewMessageType(md)
}

// eventFiles returns every registered file in the event packages, so a new
// file in either package joins the registry without being listed. The blank
// imports of both generated packages register them.
func eventFiles() []protoreflect.FileDescriptor {
	var files []protoreflect.FileDescriptor
	for _, pkg := range []protoreflect.FullName{PublicPackage, InternalPackage} {
		protoregistry.GlobalFiles.RangeFilesByPackage(pkg, func(fd protoreflect.FileDescriptor) bool {
			files = append(files, fd)
			return true
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
	return files
}

var eventPackages = map[protoreflect.FullName]eventspb.Visibility{
	PublicPackage:   eventspb.Visibility_VISIBILITY_PUBLIC,
	InternalPackage: eventspb.Visibility_VISIBILITY_INTERNAL,
}

var defaultRegistry = mustBuildDefault()

func mustBuildDefault() *Registry {
	reg, err := buildRegistry(eventFiles(), eventPackages)
	if err != nil {
		panic("events: invalid event registry: " + err.Error())
	}
	return reg
}

// Default returns the registry of every event type linked into the binary.
func Default() *Registry { return defaultRegistry }

// Lookup returns the spec registered for eventType.
func Lookup(eventType string) (Spec, bool) { return defaultRegistry.Lookup(eventType) }

// SpecFor returns the spec of msg's type.
func SpecFor(msg proto.Message) (Spec, bool) { return defaultRegistry.SpecFor(msg) }

// Specs returns every registered spec ordered by type.
func Specs() []Spec { return defaultRegistry.Specs() }
