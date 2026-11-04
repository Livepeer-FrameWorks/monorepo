"""Cross-language identity of one operation and its reachable fragments."""

from hashlib import sha256
from functools import lru_cache

from graphql import get_operation_ast, parse, print_ast
from graphql.language import Lexer, Source, TokenKind
from graphql.language.ast import (
    DocumentNode,
    OperationDefinitionNode,
    FragmentDefinitionNode,
    FragmentSpreadNode,
    FieldNode,
    InlineFragmentNode,
    SelectionSetNode,
)


def document_identity(query: str, operation_name: str | None = None) -> str:
    if len(query) > 256 * 1024:
        return _document_identity(query, operation_name)
    return _cached_identity(query, operation_name)


@lru_cache(maxsize=256)
def _cached_identity(query: str, operation_name: str | None) -> str:
    return _document_identity(query, operation_name)


def _document_identity(query: str, operation_name: str | None) -> str:
    document = parse(query)
    operation_names: set[str] = set()
    for definition in document.definitions:
        if isinstance(definition, OperationDefinitionNode):
            name = definition.name.value if definition.name else ""
            if name in operation_names:
                raise ValueError("duplicate operation")
            operation_names.add(name)
    operation = get_operation_ast(document, operation_name)
    if operation is None:
        raise ValueError("document must select exactly one GraphQL operation")
    fragments: dict[str, FragmentDefinitionNode] = {}
    for definition in document.definitions:
        if isinstance(definition, FragmentDefinitionNode):
            name = definition.name.value
            if name in fragments:
                raise ValueError("duplicate fragment")
            fragments[name] = definition
    used: set[str] = set()
    active: set[str] = set()

    def collect(selection_set: SelectionSetNode) -> None:
        for selection in selection_set.selections:
            if isinstance(selection, FragmentSpreadNode):
                name = selection.name.value
                if name in active:
                    raise ValueError("cyclic fragment")
                if name in used:
                    continue
                if name not in fragments:
                    raise ValueError(f"missing fragment {name}")
                active.add(name)
                collect(fragments[name].selection_set)
                active.remove(name)
                used.add(name)
            elif (
                isinstance(selection, (FieldNode, InlineFragmentNode))
                and selection.selection_set is not None
            ):
                collect(selection.selection_set)

    collect(operation.selection_set)
    selected = DocumentNode(
        definitions=(operation, *(fragments[name] for name in sorted(used)))
    )
    lexer = Lexer(Source(print_ast(selected)))
    canonical = "fw-operation-v1;"
    first = True
    token = lexer.advance()
    while token.kind != TokenKind.EOF:
        if first and token.kind == TokenKind.BRACE_L:
            canonical += "Name:5:query;"
        first = False
        kind = TokenKind.STRING if token.kind == TokenKind.BLOCK_STRING else token.kind
        value = token.value or ""
        canonical += f"{kind.value}:{len(value.encode('utf-8'))}:{value};"
        token = lexer.advance()
    return "sha256:" + sha256(canonical.encode("utf-8")).hexdigest()
