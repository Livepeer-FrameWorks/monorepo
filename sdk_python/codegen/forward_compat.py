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

from ariadne_codegen.plugins.base import Plugin
from graphql import FragmentDefinitionNode, GraphQLEnumType
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

    def generate_result_types_module(
        self, module: ast.Module, operation_definition: ExecutableDefinitionNode
    ) -> ast.Module:
        return _domain_fragment_names(_open_unions(module))

    def generate_fragments_module(
        self, module: ast.Module, fragments_definitions: dict[str, FragmentDefinitionNode]
    ) -> ast.Module:
        return _domain_fragment_names(_open_unions(module))

    def generate_init_module(self, module: ast.Module) -> ast.Module:
        return _domain_fragment_names(module)


def _domain_fragment_names(module: ast.Module) -> ast.Module:
    """Remove the GraphQL fragment convention's ``Fields`` suffix from
    generated Python model names and every reference to them."""

    def clean(name: str) -> str:
        return name.replace("Fields", "")

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
