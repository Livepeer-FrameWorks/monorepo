package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Markers delimit the generated blocks. Everything between a BEGIN and its END
// line is replaced on every run; the lines themselves stay put, so where the
// block lives in each file is chosen by hand.
const (
	schemaBegin = "# BEGIN GENERATED: public events (scripts/eventsgql; run make graphql-events, do not edit)"
	schemaEnd   = "# END GENERATED: public events"
	modelsBegin = "  # BEGIN GENERATED: public events (scripts/eventsgql; run make graphql-events, do not edit)"
	modelsEnd   = "  # END GENERATED: public events"
)

// Fixed bindings of the envelope types. PublicEvent is the Signalman
// TenantEvent a subscription receives; its data field holds the registered
// public message as a google.protobuf.Any, which the resolver unpacks, so the
// union binds to proto.Message and each member to its generated Go type.
const (
	publicEventModel     = "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman.TenantEvent"
	publicEventDataModel = "google.golang.org/protobuf/proto.Message"
	timestampMessage     = protoreflect.FullName("google.protobuf.Timestamp")
	// supportPrefix names public submessages and enums in GraphQL, which keeps
	// them apart from hand-written types such as the Money scalar.
	supportPrefix = "Event"
)

// eventMessage is one public registry entry.
type eventMessage struct {
	Type      string
	Aggregate string
	Desc      protoreflect.MessageDescriptor
}

// output is the generated content of both blocks, markers excluded.
type output struct {
	Schema string
	Models string
}

type generator struct {
	pkg      protoreflect.FullName
	events   map[protoreflect.FullName]string // event message -> GraphQL name
	messages map[protoreflect.FullName]protoreflect.MessageDescriptor
	enums    map[protoreflect.FullName]protoreflect.EnumDescriptor
	errs     []error
}

// generate renders the GraphQL types and gqlgen models for the public events.
// Every message must come from one proto package; referenced submessages and
// enums must be top-level declarations of that package.
func generate(events []eventMessage) (output, error) {
	if len(events) == 0 {
		return output{}, errors.New("no public events")
	}
	events = append([]eventMessage(nil), events...)
	sort.Slice(events, func(i, j int) bool { return events[i].Type < events[j].Type })

	g := &generator{
		pkg:      events[0].Desc.ParentFile().Package(),
		events:   map[protoreflect.FullName]string{},
		messages: map[protoreflect.FullName]protoreflect.MessageDescriptor{},
		enums:    map[protoreflect.FullName]protoreflect.EnumDescriptor{},
	}
	for _, ev := range events {
		if ev.Desc.ParentFile().Package() != g.pkg {
			g.errs = append(g.errs, fmt.Errorf("%s: public events must share package %s", ev.Desc.FullName(), g.pkg))
			continue
		}
		g.events[ev.Desc.FullName()] = string(ev.Desc.Name())
	}
	for _, ev := range events {
		g.collect(ev.Desc)
	}

	var schema, models strings.Builder
	g.writeEnvelope(&schema, &models, events)
	for _, ev := range events {
		fmt.Fprintf(&schema, "\n\"\"\"\n`%s` event. Aggregate: `%s`.\n\"\"\"\n", ev.Type, ev.Aggregate)
		g.writeObject(&schema, &models, string(ev.Desc.Name()), ev.Desc)
	}
	for _, name := range sortedKeys(g.messages) {
		md := g.messages[name]
		fmt.Fprintf(&schema, "\n\"%s (%s).\"\n", "Part of a public event payload", md.FullName())
		g.writeObject(&schema, &models, supportPrefix+string(md.Name()), md)
	}
	for _, name := range sortedKeys(g.enums) {
		g.writeEnum(&schema, &models, g.enums[name])
	}
	if err := errors.Join(g.errs...); err != nil {
		return output{}, err
	}
	return output{Schema: schema.String(), Models: models.String()}, nil
}

func sortedKeys[V any](m map[protoreflect.FullName]V) []protoreflect.FullName {
	keys := make([]protoreflect.FullName, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// collect records every submessage and enum reachable from md.
func (g *generator) collect(md protoreflect.MessageDescriptor) {
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		switch fd.Kind() {
		case protoreflect.EnumKind:
			ed := fd.Enum()
			if g.checkTopLevel(fd, ed) {
				g.enums[ed.FullName()] = ed
			}
		case protoreflect.MessageKind:
			sub := fd.Message()
			if sub.FullName() == timestampMessage || fd.IsMap() {
				continue
			}
			if _, isEvent := g.events[sub.FullName()]; isEvent {
				continue
			}
			if !g.checkTopLevel(fd, sub) {
				continue
			}
			if _, seen := g.messages[sub.FullName()]; !seen {
				g.messages[sub.FullName()] = sub
				g.collect(sub)
			}
		}
	}
}

func (g *generator) checkTopLevel(fd protoreflect.FieldDescriptor, d protoreflect.Descriptor) bool {
	if d.ParentFile().Package() != g.pkg {
		g.errs = append(g.errs, fmt.Errorf("%s: type %s is outside %s", fd.FullName(), d.FullName(), g.pkg))
		return false
	}
	if _, nested := d.Parent().(protoreflect.MessageDescriptor); nested {
		g.errs = append(g.errs, fmt.Errorf("%s: nested type %s is not supported; declare it at file level", fd.FullName(), d.FullName()))
		return false
	}
	return true
}

func (g *generator) writeEnvelope(schema, models *strings.Builder, events []eventMessage) {
	schema.WriteString(`"""
A public tenant event as its owning service emitted it. data is the registered
message of type, unchanged; webhooks deliver the same message.
"""
type PublicEvent {
  "Event ID, stable across redeliveries."
  id: ID!
  "Registered event type, e.g. stream.live."
  type: String!
  "When the state change committed."
  time: Time!
  "The aggregate the event belongs to, as <aggregate>/<id>, e.g. streams/<stream id>."
  subject: String!
  data: PublicEventData!
}

"""
Payload of a PublicEvent: one member per public event type. Fields of the same
name can have different types across members (reason is a different enum per
event family); alias them when one selection covers several.
"""
union PublicEventData =
`)
	for _, ev := range events {
		fmt.Fprintf(schema, "  | %s\n", ev.Desc.Name())
	}
	fmt.Fprintf(models, "  PublicEvent:\n    model:\n      - %s\n    fields:\n      time:\n        resolver: true\n      data:\n        resolver: true\n", publicEventModel)
	fmt.Fprintf(models, "  PublicEventData:\n    model:\n      - %s\n", publicEventDataModel)
}

func goImportPath(d protoreflect.Descriptor) string {
	opts := d.ParentFile().Options()
	if opts == nil {
		return ""
	}
	type goPackager interface{ GetGoPackage() string }
	gp, ok := opts.(goPackager)
	if !ok {
		return ""
	}
	path, _, _ := strings.Cut(gp.GetGoPackage(), ";")
	return path
}

func (g *generator) goType(d protoreflect.Descriptor) string {
	path := goImportPath(d)
	if path == "" {
		g.errs = append(g.errs, fmt.Errorf("%s: file %s has no go_package", d.FullName(), d.ParentFile().Path()))
	}
	return path + "." + string(d.Name())
}

func (g *generator) writeObject(schema, models *strings.Builder, gqlName string, md protoreflect.MessageDescriptor) {
	fmt.Fprintf(schema, "type %s {\n", gqlName)
	var resolverFields []string
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		gqlType, needsResolver, err := g.fieldType(fd)
		if err != nil {
			g.errs = append(g.errs, err)
			continue
		}
		fmt.Fprintf(schema, "  %s: %s\n", fd.JSONName(), gqlType)
		if needsResolver {
			resolverFields = append(resolverFields, fd.JSONName())
		}
	}
	schema.WriteString("}\n")

	fmt.Fprintf(models, "  %s:\n    model:\n      - %s\n", gqlName, g.goType(md))
	if len(resolverFields) > 0 {
		models.WriteString("    fields:\n")
		for _, name := range resolverFields {
			fmt.Fprintf(models, "      %s:\n        resolver: true\n", name)
		}
	}
}

// fieldType maps a proto field to its GraphQL type. proto3 scalars always
// carry a value, so they are non-null; message fields may be unset. Timestamp
// fields need a resolver because Time binds time.Time, not
// *timestamppb.Timestamp.
func (g *generator) fieldType(fd protoreflect.FieldDescriptor) (string, bool, error) {
	if fd.IsMap() {
		return "", false, fmt.Errorf("%s: map fields are not supported", fd.FullName())
	}
	if oneof := fd.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
		return "", false, fmt.Errorf("%s: oneof fields are not supported", fd.FullName())
	}
	var base string
	nonNull := true
	resolver := false
	switch fd.Kind() {
	case protoreflect.StringKind:
		base = "String"
		if strings.HasSuffix(string(fd.Name()), "_id") {
			base = "ID"
		}
	case protoreflect.BoolKind:
		base = "Boolean"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		base = "Int"
	case protoreflect.DoubleKind:
		base = "Float"
	case protoreflect.EnumKind:
		base = supportPrefix + string(fd.Enum().Name())
	case protoreflect.MessageKind:
		sub := fd.Message()
		switch {
		case sub.FullName() == timestampMessage:
			base, resolver = "Time", true
		default:
			if name, isEvent := g.events[sub.FullName()]; isEvent {
				base = name
			} else {
				base = supportPrefix + string(sub.Name())
			}
		}
		nonNull = false
	default:
		return "", false, fmt.Errorf("%s: %s fields have no GraphQL mapping", fd.FullName(), fd.Kind())
	}
	if fd.HasPresence() && fd.Kind() != protoreflect.MessageKind {
		nonNull = false
	}
	if fd.IsList() {
		if resolver {
			return "", false, fmt.Errorf("%s: repeated Timestamp fields are not supported", fd.FullName())
		}
		return "[" + base + "!]!", false, nil
	}
	if nonNull {
		return base + "!", resolver, nil
	}
	return base, resolver, nil
}

func (g *generator) writeEnum(schema, models *strings.Builder, ed protoreflect.EnumDescriptor) {
	gqlName := supportPrefix + string(ed.Name())
	goType := g.goType(ed)
	pkgPath, _, _ := strings.Cut(goType, "."+string(ed.Name()))
	fmt.Fprintf(schema, "\n\"Values of %s, named as in the proto and the webhook JSON.\"\nenum %s {\n", ed.FullName(), gqlName)
	fmt.Fprintf(models, "  %s:\n    model:\n      - %s\n    enum_values:\n", gqlName, goType)
	values := ed.Values()
	for i := 0; i < values.Len(); i++ {
		name := string(values.Get(i).Name())
		fmt.Fprintf(schema, "  %s\n", name)
		fmt.Fprintf(models, "      %s:\n        value: %s.%s_%s\n", name, pkgPath, ed.Name(), name)
	}
	schema.WriteString("}\n")
}

// splice replaces the lines between begin and end in content with block.
func splice(content, begin, end, block string) (string, error) {
	start := strings.Index(content, begin+"\n")
	if start < 0 {
		return "", fmt.Errorf("marker %q not found", begin)
	}
	bodyStart := start + len(begin) + 1
	stop := strings.Index(content[bodyStart:], end+"\n")
	if stop < 0 {
		return "", fmt.Errorf("marker %q not found after %q", end, begin)
	}
	if strings.Count(content, begin+"\n") != 1 {
		return "", fmt.Errorf("marker %q appears more than once", begin)
	}
	return content[:bodyStart] + block + content[bodyStart+stop:], nil
}

// handWrittenTypeNames returns the type names declared in schema outside the
// generated block.
func handWrittenTypeNames(schema string) map[string]bool {
	names := map[string]bool{}
	inBlock := false
	for _, line := range strings.Split(schema, "\n") {
		switch {
		case line == schemaBegin:
			inBlock = true
			continue
		case line == schemaEnd:
			inBlock = false
			continue
		case inBlock:
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "type", "enum", "union", "input", "interface", "scalar":
			names[strings.TrimSuffix(fields[1], "{")] = true
		}
	}
	return names
}

// generatedTypeNames returns the type names the schema block declares.
func generatedTypeNames(block string) []string {
	var names []string
	for _, line := range strings.Split(block, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "type" || fields[0] == "enum" || fields[0] == "union") {
			names = append(names, fields[1])
		}
	}
	return names
}
