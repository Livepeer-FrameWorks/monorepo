"""ariadne-codegen plugin: generated models accept union members and enum
values a newer server adds.

ariadne-codegen emits each union field as a pydantic discriminated union and
each enum as a plain Enum, and both reject a value they were not generated
with. The SDK supports a range of server releases, so this plugin rewrites:

- every enum to subclass livepeer_frameworks._forward.OpenEnum, which decodes
  an unknown value to a member carrying the server's string;
- every discriminated union field, Union[A, B] = Field(discriminator=...), to
  Annotated[Union[A, B, UnknownMember], OpenUnion()], which decodes an unknown
  __typename to an UnknownMember carrying the raw JSON.

make graphql-sdk-py puts this directory on PYTHONPATH and codegen/*.toml
lists the plugin."""

from __future__ import annotations

import ast
import functools
import json
from pathlib import Path

from ariadne_codegen.plugins.base import Plugin
from graphql import (
    FieldNode,
    FragmentDefinitionNode,
    GraphQLEnumType,
    GraphQLInputField,
    GraphQLInterfaceType,
    GraphQLNamedType,
    GraphQLSchema,
    GraphQLUnionType,
    InlineFragmentNode,
    OperationDefinitionNode,
    OperationType,
    SelectionSetNode,
    get_named_type,
    parse,
)
from graphql.language import ExecutableDefinitionNode

# The generated modules live in livepeer_frameworks._generated.graphql, three
# package levels below livepeer_frameworks._forward.
_RUNTIME_MODULE = "_forward"
_RUNTIME_LEVEL = 3
_DISCRIMINATOR = "discriminator"


class ForwardCompatPlugin(Plugin):
    def generate_enum(self, class_def: ast.ClassDef, enum_type: GraphQLEnumType) -> ast.ClassDef:
        class_def.bases = [ast.Name(id="OpenEnum")]
        return class_def

    def generate_enums_module(self, module: ast.Module) -> ast.Module:
        return _with_imports(module, runtime=["OpenEnum"])

    def generate_input_field(
        self,
        field_implementation: ast.AnnAssign,
        input_field: GraphQLInputField,
        field_name: str,
    ) -> ast.AnnAssign:
        del field_name
        return _with_description(field_implementation, input_field.description)

    def generate_result_field(
        self,
        field_implementation: ast.AnnAssign,
        operation_definition: ExecutableDefinitionNode,
        field: FieldNode,
    ) -> ast.AnnAssign:
        schema_field = _find_field(self.schema, operation_definition, field)
        return _with_description(
            field_implementation, schema_field.description if schema_field else None
        )

    def generate_result_class(
        self,
        class_def: ast.ClassDef,
        operation_definition: ExecutableDefinitionNode,
        selection_set: SelectionSetNode,
    ) -> ast.ClassDef:
        type_ = _find_selection_type(self.schema, operation_definition, selection_set)
        if type_ and type_.description:
            class_def.body.insert(0, ast.Expr(value=ast.Constant(value=type_.description)))
        return class_def

    def generate_client_method(
        self,
        method_def: ast.FunctionDef | ast.AsyncFunctionDef,
        operation_definition: OperationDefinitionNode,
    ) -> ast.FunctionDef | ast.AsyncFunctionDef:
        description = _operation_description(self.schema, operation_definition)
        if description:
            method_def.body.insert(0, ast.Expr(value=ast.Constant(value=description)))
        return method_def

    def generate_result_types_module(
        self, module: ast.Module, operation_definition: ExecutableDefinitionNode
    ) -> ast.Module:
        return _domain_fragment_names(
            _add_attribute_docstrings(_open_unions(module)), self._fragment_names()
        )

    def generate_fragments_module(
        self, module: ast.Module, fragments_definitions: dict[str, FragmentDefinitionNode]
    ) -> ast.Module:
        return _domain_fragment_names(
            _add_attribute_docstrings(_open_unions(module)), self._fragment_names()
        )

    def generate_init_module(self, module: ast.Module) -> ast.Module:
        return _domain_fragment_names(module, self._fragment_names())

    def _fragment_names(self) -> list[str]:
        """The fragment names of the operation documents this run reads,
        longest first."""
        cached: list[str] | None = getattr(self, "_fragments", None)
        if cached is None:
            settings = self.config_dict.get("tool", {}).get("ariadne-codegen", self.config_dict)
            names = set()
            for path in Path(settings["queries_path"]).rglob("*.graphql"):
                for definition in parse(path.read_text()).definitions:
                    if isinstance(definition, FragmentDefinitionNode):
                        names.add(definition.name.value)
            cached = sorted(names, key=lambda n: (-len(n), n))
            self._fragments = cached
        return cached


def _domain_fragment_names(module: ast.Module, fragments: list[str]) -> ast.Module:
    """Remove the GraphQL fragment convention's ``Fields`` suffix from
    generated Python model names and every reference to them: StreamFields
    becomes Stream and StreamFieldsPlaybackPolicy StreamPlaybackPolicy. Only
    a fragment name's suffix is removed, so a class for a field named fields
    (MediaPlacementErrorFields) keeps its name."""

    def clean(name: str) -> str:
        # ariadne-codegen writes forward references as names in quotes.
        if len(name) > 1 and name[0] == name[-1] == '"':
            return f'"{clean(name[1:-1])}"'
        for fragment in fragments:
            if fragment.endswith("Fields") and name.startswith(fragment):
                return fragment[: -len("Fields")] + name[len(fragment) :]
        return name

    class Rename(ast.NodeTransformer):
        def visit_ClassDef(self, node: ast.ClassDef) -> ast.AST:
            node.name = clean(node.name)
            return self.generic_visit(node)

        def visit_Name(self, node: ast.Name) -> ast.AST:
            node.id = clean(node.id)
            return node

        def visit_alias(self, node: ast.alias) -> ast.AST:
            node.name = clean(node.name)
            if node.asname:
                node.asname = clean(node.asname)
            return node

        def visit_Constant(self, node: ast.Constant) -> ast.AST:
            if (
                isinstance(node.value, str)
                and node.value.isidentifier()
                and node.value[:1].isupper()
            ):
                node.value = clean(node.value)
            return node

    result = Rename().visit(module)
    assert isinstance(result, ast.Module)
    return result


def _with_description(field: ast.AnnAssign, description: str | None) -> ast.AnnAssign:
    """Store a schema field description in Pydantic's JSON-schema metadata."""
    if not description:
        return field
    keyword = ast.keyword(arg="description", value=ast.Constant(value=description))
    if isinstance(field.value, ast.Call) and _name(field.value.func) == "Field":
        if not any(item.arg == "description" for item in field.value.keywords):
            field.value.keywords.append(keyword)
        return field

    keywords = [keyword]
    if field.value is not None:
        keywords.insert(0, ast.keyword(arg="default", value=field.value))
    field.value = ast.Call(func=ast.Name(id="Field"), args=[], keywords=keywords)
    return field


def _add_attribute_docstrings(module: ast.Module) -> ast.Module:
    """Mirror Pydantic descriptions as attribute docstrings for IDE hovers."""
    for node in ast.walk(module):
        if not isinstance(node, ast.ClassDef):
            continue
        body: list[ast.stmt] = []
        for statement in node.body:
            body.append(statement)
            if not isinstance(statement, ast.AnnAssign) or not isinstance(statement.value, ast.Call):
                continue
            for keyword in statement.value.keywords:
                if (
                    keyword.arg == "description"
                    and isinstance(keyword.value, ast.Constant)
                    and isinstance(keyword.value.value, str)
                ):
                    body.append(ast.Expr(value=ast.Constant(value=keyword.value.value)))
                    break
        node.body = body
    return module


def _definition_root(
    schema: GraphQLSchema, definition: ExecutableDefinitionNode
) -> GraphQLNamedType | None:
    if isinstance(definition, FragmentDefinitionNode):
        return schema.get_type(definition.type_condition.name.value)
    if definition.operation == OperationType.QUERY:
        return schema.query_type
    if definition.operation == OperationType.MUTATION:
        return schema.mutation_type
    return schema.subscription_type


def _same_node(left: object, right: object) -> bool:
    if left is right:
        return True
    left_loc = getattr(left, "loc", None)
    right_loc = getattr(right, "loc", None)
    return bool(
        left_loc
        and right_loc
        and left_loc.start == right_loc.start
        and left_loc.end == right_loc.end
    )


def _field_map(type_: GraphQLNamedType | None) -> dict:
    fields = getattr(type_, "fields", None)
    return fields if isinstance(fields, dict) else {}


def _inline_type(
    schema: GraphQLSchema, selection: InlineFragmentNode, fallback: GraphQLNamedType | None
) -> GraphQLNamedType | None:
    if selection.type_condition:
        return schema.get_type(selection.type_condition.name.value)
    return fallback


def _find_field(
    schema: GraphQLSchema,
    definition: ExecutableDefinitionNode,
    target: FieldNode,
):
    def visit(selection_set: SelectionSetNode, parent: GraphQLNamedType | None):
        for selection in selection_set.selections:
            if isinstance(selection, FieldNode):
                field = _field_map(parent).get(selection.name.value)
                if _same_node(selection, target):
                    return field
                if selection.selection_set and field:
                    found = visit(selection.selection_set, get_named_type(field.type))
                    if found:
                        return found
            elif isinstance(selection, InlineFragmentNode):
                found = visit(selection.selection_set, _inline_type(schema, selection, parent))
                if found:
                    return found
        return None

    return visit(definition.selection_set, _definition_root(schema, definition))


def _find_selection_type(
    schema: GraphQLSchema,
    definition: ExecutableDefinitionNode,
    target: SelectionSetNode,
) -> GraphQLNamedType | None:
    def visit(
        selection_set: SelectionSetNode, parent: GraphQLNamedType | None
    ) -> GraphQLNamedType | None:
        if _same_node(selection_set, target):
            return parent
        for selection in selection_set.selections:
            if isinstance(selection, FieldNode) and selection.selection_set:
                field = _field_map(parent).get(selection.name.value)
                if field:
                    found = visit(selection.selection_set, get_named_type(field.type))
                    if found:
                        return found
            elif isinstance(selection, InlineFragmentNode):
                found = visit(
                    selection.selection_set, _inline_type(schema, selection, parent)
                )
                if found:
                    return found
        return None

    return visit(definition.selection_set, _definition_root(schema, definition))


_TARGETS_FILE = Path(__file__).resolve().parents[2] / "pkg/graphql/public/generated/targets.json"


@functools.cache
def _targets() -> dict[str, str]:
    """Each public operation's target, from scripts/sdkcontract (make
    generate-ops)."""
    targets: dict[str, str] = json.loads(_TARGETS_FILE.read_text())["targets"]
    return targets


def _operation_description(
    schema: GraphQLSchema, definition: OperationDefinitionNode
) -> str | None:
    """The description of the field the operation targets. A target is
    kind.field.field, with a member type name after a field of union or
    interface type (query.node.InfrastructureNode.metricsConnection)."""
    name = definition.name.value if definition.name else ""
    target = _targets().get(name)
    if target is None:
        raise ValueError(f"{name}: no target in {_TARGETS_FILE}; run make generate-ops")
    parent: GraphQLNamedType | None = _definition_root(schema, definition)
    description = None
    for segment in target.split(".")[1:]:
        if isinstance(parent, (GraphQLUnionType, GraphQLInterfaceType)):
            member = next(
                (t for t in schema.get_possible_types(parent) if t.name == segment), None
            )
            if member is not None:
                parent = member
                continue
        field = _field_map(parent).get(segment)
        if field is None:
            raise ValueError(f"{name}: target {target} is not in the schema")
        description = field.description
        parent = get_named_type(field.type)
    return description


def _open_unions(module: ast.Module) -> ast.Module:
    changed = False
    for node in ast.walk(module):
        if isinstance(node, ast.ClassDef):
            for stmt in node.body:
                if isinstance(stmt, ast.AnnAssign):
                    changed = _open_field(stmt) or changed
    if not changed:
        return module
    return _with_imports(module, runtime=["OpenUnion", "UnknownMember"], typing=["Annotated"])


def _open_field(field: ast.AnnAssign) -> bool:
    """Rewrites one class field whose union is discriminated, either by the
    field's Field(discriminator=...) or by an Annotated[Union[...],
    Field(discriminator=...)] inside the annotation (a list of unions)."""
    changed = False
    if isinstance(field.value, ast.Call) and _drop_discriminator(field.value):
        union = _find_union(field.annotation)
        if union is None:
            raise ValueError(f"discriminated field without a Union: {ast.unparse(field)}")
        field.annotation = _replace(field.annotation, union, _open(union))
        if not field.value.args and not field.value.keywords:
            field.value = None
        changed = True
    for node in list(ast.walk(field.annotation)):
        if _is_discriminated_annotated(node):
            assert isinstance(node, ast.Subscript) and isinstance(node.slice, ast.Tuple)
            field.annotation = _replace(field.annotation, node, _open(node.slice.elts[0]))
            changed = True
    return changed


def _drop_discriminator(call: ast.Call) -> bool:
    kept = [k for k in call.keywords if k.arg != _DISCRIMINATOR]
    dropped = len(kept) != len(call.keywords)
    call.keywords = kept
    return dropped


def _is_discriminated_annotated(node: ast.AST) -> bool:
    if not (isinstance(node, ast.Subscript) and _name(node.value) == "Annotated"):
        return False
    if not isinstance(node.slice, ast.Tuple):
        return False
    return any(
        isinstance(meta, ast.Call) and any(k.arg == _DISCRIMINATOR for k in meta.keywords)
        for meta in node.slice.elts[1:]
    )


def _find_union(node: ast.expr) -> ast.Subscript | None:
    for child in ast.walk(node):
        if isinstance(child, ast.Subscript) and _name(child.value) == "Union":
            return child
    return None


def _open(union: ast.expr) -> ast.Subscript:
    if not (isinstance(union, ast.Subscript) and _name(union.value) == "Union"):
        raise ValueError(f"expected a Union, got {ast.unparse(union)}")
    members = union.slice.elts if isinstance(union.slice, ast.Tuple) else [union.slice]
    opened = ast.Subscript(
        value=ast.Name(id="Union"),
        slice=ast.Tuple(elts=[*members, ast.Name(id="UnknownMember")]),
    )
    return ast.Subscript(
        value=ast.Name(id="Annotated"),
        slice=ast.Tuple(elts=[opened, ast.Call(func=ast.Name(id="OpenUnion"), args=[], keywords=[])]),
    )


def _replace(root: ast.expr, old: ast.AST, new: ast.expr) -> ast.expr:
    if root is old:
        return new

    class Replace(ast.NodeTransformer):
        def visit(self, node: ast.AST) -> ast.AST:
            return new if node is old else super().visit(node)

    result = Replace().visit(root)
    assert isinstance(result, ast.expr)
    return result


def _name(node: ast.expr) -> str | None:
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        return node.attr
    return None


def _with_imports(module: ast.Module, *, runtime: list[str], typing: list[str] | None = None) -> ast.Module:
    """Prepends the imports; the generator's ruff pass merges and sorts them
    and drops ones left unused."""
    imports: list[ast.stmt] = [
        ast.ImportFrom(module=_RUNTIME_MODULE, names=[ast.alias(name=n) for n in runtime], level=_RUNTIME_LEVEL)
    ]
    if typing:
        imports.append(ast.ImportFrom(module="typing", names=[ast.alias(name=n) for n in typing], level=0))
    module.body = [*imports, *module.body]
    return module
