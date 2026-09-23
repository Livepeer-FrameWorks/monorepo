package main

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// schema-compat compares the public schema of the latest release tag with the
// working tree's. The contract is everything a client can reach from the
// released schema's root operation types, whether or not an SDK operation
// selects it today: every output field, every argument, every input type and
// input field, every enum value, every union member, and every interface
// implementation. What is outside it:
//
//   - a field marked @experimental in the released schema, and whatever only
//     such fields reach (a stable field becoming @experimental is breaking);
//   - an element @deprecated in the minimum server of every live SDK line
//     (the policy's deprecation rule), and whatever only such elements reach;
//   - @internal fields, which the public schema does not contain.
//
// The contract starts at the oldest minimum server of the live SDK lines
// (support.yaml). A release below it is served by no live SDK line, so while
// the latest tag is older the check reports the diff and does not fail.

// schemaDiff is the result of comparing two public schemas. Info holds the
// additions and other compatible changes, reported for release notes.
type schemaDiff struct {
	Breaking []string
	Info     []string
}

type schemaComparison struct {
	old, new *ast.Schema
	// newFull is the working-tree schema including @internal fields, used to
	// say why a field left the public schema. It may be nil.
	newFull *ast.Schema
	// minimums are the tagged minimum-server schemas of the live SDK lines.
	minimums []*ast.Schema

	reachable   map[string]bool
	inputEnums  map[string]bool
	outputEnums map[string]bool
	breaking    map[string]bool
	info        map[string]bool
}

// diffPublicSchemas lists the changes from oldS to newS. An element is exempt
// from the deprecation rule only when every schema in minimums has it
// deprecated; with no minimums nothing is exempt.
func diffPublicSchemas(oldS, newS, newFull *ast.Schema, minimums []*ast.Schema) schemaDiff {
	c := &schemaComparison{
		old: oldS, new: newS, newFull: newFull, minimums: minimums,
		reachable: map[string]bool{}, inputEnums: map[string]bool{}, outputEnums: map[string]bool{},
		breaking: map[string]bool{}, info: map[string]bool{},
	}
	c.walk()
	for _, name := range sortedKeys(c.reachable) {
		c.compareType(c.old.Types[name], c.new.Types[name])
	}
	for name, def := range c.new.Types {
		if !def.BuiltIn && !strings.HasPrefix(name, "__") && c.old.Types[name] == nil {
			c.addInfo("type %s was added", name)
		}
	}
	return schemaDiff{Breaking: sortedKeys(c.breaking), Info: sortedKeys(c.info)}
}

func (c *schemaComparison) addBreaking(format string, args ...any) {
	c.breaking[fmt.Sprintf(format, args...)] = true
}

func (c *schemaComparison) addInfo(format string, args ...any) {
	c.info[fmt.Sprintf(format, args...)] = true
}

// walk marks the old schema's types a client can reach from the root types,
// and the enums reached in input and in output positions.
func (c *schemaComparison) walk() {
	var queue []string
	add := func(name string) {
		def := c.old.Types[name]
		if def == nil || def.BuiltIn || c.reachable[name] {
			return
		}
		c.reachable[name] = true
		queue = append(queue, name)
	}
	reach := func(t *ast.Type, input bool) {
		name := t.Name()
		if def := c.old.Types[name]; def != nil && def.Kind == ast.Enum {
			if input {
				c.inputEnums[name] = true
			} else {
				c.outputEnums[name] = true
			}
		}
		add(name)
	}
	for _, root := range []*ast.Definition{c.old.Query, c.old.Mutation, c.old.Subscription} {
		if root != nil {
			add(root.Name)
		}
	}
	for len(queue) > 0 {
		def := c.old.Types[queue[0]]
		queue = queue[1:]
		switch def.Kind {
		case ast.Object, ast.Interface:
			// A fragment on an implemented interface is valid inside the
			// object's selection, and every implementation of an interface
			// can be returned where the interface is.
			for _, iface := range def.Interfaces {
				add(iface)
			}
			if def.Kind == ast.Interface {
				for _, impl := range c.old.PossibleTypes[def.Name] {
					add(impl.Name)
				}
			}
			for _, field := range def.Fields {
				if c.fieldExemption(def.Name, field) != "" {
					continue
				}
				reach(field.Type, false)
				for _, arg := range field.Arguments {
					if !c.deprecatedArgument(def.Name, field.Name, arg.Name) {
						reach(arg.Type, true)
					}
				}
			}
		case ast.Union:
			for _, member := range def.Types {
				add(member)
			}
		case ast.InputObject:
			for _, field := range def.Fields {
				if !c.deprecatedField(def.Name, field.Name) {
					reach(field.Type, true)
				}
			}
		}
	}
}

func fieldOf(s *ast.Schema, typeName, field string) *ast.FieldDefinition {
	if s == nil || s.Types[typeName] == nil {
		return nil
	}
	return s.Types[typeName].Fields.ForName(field)
}

// deprecatedAtMinimums reports whether every live line's minimum server has
// the element find locates, deprecated.
func (c *schemaComparison) deprecatedAtMinimums(find func(*ast.Schema) (ast.DirectiveList, bool)) bool {
	if len(c.minimums) == 0 {
		return false
	}
	for _, s := range c.minimums {
		dirs, ok := find(s)
		if !ok || !deprecated(dirs) {
			return false
		}
	}
	return true
}

func (c *schemaComparison) deprecatedField(typeName, field string) bool {
	return c.deprecatedAtMinimums(func(s *ast.Schema) (ast.DirectiveList, bool) {
		if f := fieldOf(s, typeName, field); f != nil {
			return f.Directives, true
		}
		return nil, false
	})
}

func (c *schemaComparison) deprecatedArgument(typeName, field, arg string) bool {
	return c.deprecatedAtMinimums(func(s *ast.Schema) (ast.DirectiveList, bool) {
		if f := fieldOf(s, typeName, field); f != nil {
			if a := f.Arguments.ForName(arg); a != nil {
				return a.Directives, true
			}
		}
		return nil, false
	})
}

func (c *schemaComparison) deprecatedEnumValue(enum, value string) bool {
	return c.deprecatedAtMinimums(func(s *ast.Schema) (ast.DirectiveList, bool) {
		if def := s.Types[enum]; def != nil {
			if v := def.EnumValues.ForName(value); v != nil {
				return v.Directives, true
			}
		}
		return nil, false
	})
}

// fieldExemption returns why an output field is outside the contract, or ""
// when it is inside. Only the released schema's @experimental counts: marking
// a stable field experimental is itself a breaking change, or a field could
// leave the contract in one release and disappear in the next.
func (c *schemaComparison) fieldExemption(typeName string, oldField *ast.FieldDefinition) string {
	if _, _, ok := experimentalMark(oldField); ok {
		return "experimental"
	}
	if c.deprecatedField(typeName, oldField.Name) {
		return "deprecated at every live minimum server"
	}
	return ""
}

func (c *schemaComparison) compareType(oldDef, newDef *ast.Definition) {
	if newDef == nil {
		c.addBreaking("type %s was removed", oldDef.Name)
		return
	}
	if oldDef.Kind != newDef.Kind {
		c.addBreaking("type %s changed from %s to %s", oldDef.Name, oldDef.Kind, newDef.Kind)
		return
	}
	switch oldDef.Kind {
	case ast.Object, ast.Interface:
		c.compareOutputFields(oldDef, newDef)
		if oldDef.Kind == ast.Interface {
			c.compareMembers("interface", oldDef.Name, "implementation", typeNames(c.old.PossibleTypes[oldDef.Name]), typeNames(c.new.PossibleTypes[newDef.Name]))
		}
	case ast.Union:
		c.compareMembers("union", oldDef.Name, "member", oldDef.Types, newDef.Types)
	case ast.Enum:
		c.compareEnum(oldDef, newDef)
	case ast.InputObject:
		c.compareInputValues("input field", oldDef.Name, inputFields(oldDef.Fields), inputFields(newDef.Fields), c.deprecatedField)
	}
}

func typeNames(defs []*ast.Definition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

func (c *schemaComparison) compareOutputFields(oldDef, newDef *ast.Definition) {
	for _, of := range oldDef.Fields {
		key := oldDef.Name + "." + of.Name
		nf := newDef.Fields.ForName(of.Name)
		if why := c.fieldExemption(oldDef.Name, of); why != "" {
			switch {
			case nf == nil:
				c.addInfo("%s was removed (%s, not checked)", key, why)
			case of.Type.String() != nf.Type.String():
				c.addInfo("%s changed type from %s to %s (%s, not checked)", key, of.Type, nf.Type, why)
			}
			continue
		}
		if nf == nil {
			c.addBreaking("%s was removed%s", key, c.internalNote(oldDef.Name, of.Name))
			continue
		}
		if _, _, ok := experimentalMark(nf); ok {
			c.addBreaking("%s became @experimental; a stable field cannot leave the compatibility contract", key)
		}
		switch {
		case !outputCompatible(of.Type, nf.Type):
			c.addBreaking("%s changed type from %s to %s", key, of.Type, nf.Type)
		case of.Type.String() != nf.Type.String():
			c.addInfo("%s changed type from %s to %s (compatible)", key, of.Type, nf.Type)
		}
		deprecatedArg := func(_, arg string) bool { return c.deprecatedArgument(oldDef.Name, of.Name, arg) }
		c.compareInputValues("argument", key, inputArguments(of.Arguments), inputArguments(nf.Arguments), deprecatedArg)
	}
	for _, nf := range newDef.Fields {
		if oldDef.Fields.ForName(nf.Name) == nil {
			c.addInfo("%s.%s was added", newDef.Name, nf.Name)
		}
	}
}

// internalNote explains a public field's removal when the working tree still
// has it as an @internal field.
func (c *schemaComparison) internalNote(typeName, field string) string {
	if f := fieldOf(c.newFull, typeName, field); f != nil {
		if _, internal := internalReason(f); internal {
			return " (now @internal)"
		}
	}
	return ""
}

// inputValue is an argument or an input field.
type inputValue struct {
	name       string
	typ        *ast.Type
	hasDefault bool
}

func (v inputValue) required() bool { return v.typ.NonNull && !v.hasDefault }

func inputArguments(args ast.ArgumentDefinitionList) []inputValue {
	out := make([]inputValue, 0, len(args))
	for _, a := range args {
		out = append(out, inputValue{name: a.Name, typ: a.Type, hasDefault: a.DefaultValue != nil})
	}
	return out
}

func inputFields(fields ast.FieldList) []inputValue {
	out := make([]inputValue, 0, len(fields))
	for _, f := range fields {
		out = append(out, inputValue{name: f.Name, typ: f.Type, hasDefault: f.DefaultValue != nil})
	}
	return out
}

func findInput(values []inputValue, name string) *inputValue {
	for i := range values {
		if values[i].name == name {
			return &values[i]
		}
	}
	return nil
}

// compareInputValues compares the arguments of a field or the fields of an
// input type. A value a client could send to the old schema must still be
// accepted: nothing it may set is removed or retyped, no nullable position
// becomes required, and nothing new is required.
func (c *schemaComparison) compareInputValues(kind, owner string, oldVals, newVals []inputValue, exempt func(owner, name string) bool) {
	label := func(name string) string {
		if kind == "argument" {
			return fmt.Sprintf("argument %s(%s:)", owner, name)
		}
		return fmt.Sprintf("input field %s.%s", owner, name)
	}
	for _, ov := range oldVals {
		nv := findInput(newVals, ov.name)
		if exempt(owner, ov.name) {
			if nv == nil {
				c.addInfo("%s was removed (deprecated at every live minimum server, not checked)", label(ov.name))
			}
			continue
		}
		switch {
		case nv == nil:
			c.addBreaking("%s was removed", label(ov.name))
		case !inputCompatible(ov.typ, nv.typ):
			c.addBreaking("%s changed type from %s to %s", label(ov.name), ov.typ, nv.typ)
		case !ov.required() && nv.required():
			c.addBreaking("%s became required: its default was removed", label(ov.name))
		case ov.typ.String() != nv.typ.String():
			c.addInfo("%s changed type from %s to %s (compatible)", label(ov.name), ov.typ, nv.typ)
		}
	}
	for _, nv := range newVals {
		if findInput(oldVals, nv.name) != nil {
			continue
		}
		if nv.required() {
			c.addBreaking("%s was added as required without a default", label(nv.name))
		} else {
			c.addInfo("%s was added", label(nv.name))
		}
	}
}

// compareMembers compares union members or interface implementations. A
// removed one breaks every inline fragment or fragment spread on it inside a
// selection of the union or interface: the document stops validating.
func (c *schemaComparison) compareMembers(kind, name, member string, oldMembers, newMembers []string) {
	now := map[string]bool{}
	for _, m := range newMembers {
		now[m] = true
	}
	was := map[string]bool{}
	for _, m := range oldMembers {
		was[m] = true
		if !now[m] {
			c.addBreaking("%s %s lost %s %s; fragments on %s inside its selections no longer validate", kind, name, member, m, m)
		}
	}
	for _, m := range newMembers {
		if !was[m] {
			c.addInfo("%s %s gained %s %s", kind, name, member, m)
		}
	}
}

// compareEnum treats a removed value as breaking in either position. As
// input, a client sending it is rejected. As output, the regenerated SDK of
// the same line drops the constant, so consumer code that names it (an
// exhaustive switch, a comparison) stops compiling on a patch upgrade;
// retiring a value therefore goes through @deprecated like a field.
func (c *schemaComparison) compareEnum(oldDef, newDef *ast.Definition) {
	var positions []string
	if c.outputEnums[oldDef.Name] {
		positions = append(positions, "returned")
	}
	if c.inputEnums[oldDef.Name] {
		positions = append(positions, "accepted as input")
	}
	where := strings.Join(positions, " and ")
	for _, v := range oldDef.EnumValues {
		if newDef.EnumValues.ForName(v.Name) != nil {
			continue
		}
		if c.deprecatedEnumValue(oldDef.Name, v.Name) {
			c.addInfo("enum value %s.%s was removed (deprecated at every live minimum server, not checked)", oldDef.Name, v.Name)
			continue
		}
		c.addBreaking("enum value %s.%s, %s, was removed", oldDef.Name, v.Name, where)
	}
	for _, v := range newDef.EnumValues {
		if oldDef.EnumValues.ForName(v.Name) == nil {
			c.addInfo("enum value %s.%s was added", newDef.Name, v.Name)
		}
	}
}

// contractBaseline is the release the compatibility contract starts at: the
// oldest minimum server of the live SDK lines. It is the existing support.yaml
// field rather than a separate setting because it means the same thing: no
// live SDK line supports a release below it, so a change relative to such a
// release breaks no supported client.
func contractBaseline(support *supportFile) semver {
	var oldest *semver
	for _, line := range support.Lines {
		if line.Status != lineLive {
			continue
		}
		min, _ := parseSemver(line.MinServer)
		if oldest == nil || min.Less(*oldest) {
			oldest = &min
		}
	}
	if oldest == nil {
		return semver{}
	}
	return *oldest
}

// schemaCompatVerdict fails on breaking changes once latest, the release the
// working tree is compared with, is at or above the contract baseline.
// Before that the diff is a report only.
func schemaCompatVerdict(diff schemaDiff, latest, baseline semver) error {
	if latest.Less(baseline) || len(diff.Breaking) == 0 {
		return nil
	}
	return failList(fmt.Sprintf("breaking changes to the public schema since %s", latest), diff.Breaking)
}

func runSchemaCompat(repo string) error {
	support, err := loadSupport(repo)
	if err != nil {
		return err
	}
	matrix, err := loadMatrix(repo)
	if err != nil {
		return err
	}
	if len(matrix.Tags) == 0 {
		fmt.Println("sdkcontract: no release tag reachable; nothing to compare against")
		return nil
	}
	latest := matrix.Tags[len(matrix.Tags)-1]
	baseline := contractBaseline(support)
	cache := newSchemaCache(repo)
	oldS, err := cache.get(latest.String())
	if err != nil {
		return err
	}
	newS, err := cache.get(headRef)
	if err != nil {
		return err
	}
	newFull, err := loadFullSchema(repo, headRef)
	if err != nil {
		return err
	}

	tagged := map[semver]bool{}
	for _, t := range matrix.Tags {
		tagged[t] = true
	}
	var minimums []*ast.Schema
	for _, line := range support.Lines {
		min, _ := parseSemver(line.MinServer)
		if line.Status != lineLive || !tagged[min] {
			continue
		}
		s, err := cache.get(min.String())
		if err != nil {
			return err
		}
		minimums = append(minimums, s)
	}

	diff := diffPublicSchemas(oldS, newS, newFull, minimums)
	fmt.Printf("sdkcontract: public schema %s -> working tree (%s): %d breaking, %d compatible changes\n",
		latest, matrix.Pending, len(diff.Breaking), len(diff.Info))
	if len(diff.Info) > 0 {
		fmt.Printf("compatible changes:\n  %s\n", strings.Join(diff.Info, "\n  "))
	}
	if latest.Less(baseline) {
		if len(diff.Breaking) > 0 {
			fmt.Printf("breaking changes (report only):\n  %s\n", strings.Join(diff.Breaking, "\n  "))
		}
		fmt.Printf("sdkcontract: report only: the contract starts at %s, the oldest live min_server in %s, which is not tagged yet\n", baseline, supportPath)
		return nil
	}
	if err := schemaCompatVerdict(diff, latest, baseline); err != nil {
		return err
	}
	fmt.Printf("sdkcontract: the working tree keeps the public schema of %s compatible\n", latest)
	return nil
}
