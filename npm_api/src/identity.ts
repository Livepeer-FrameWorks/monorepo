import { sha256 } from "@noble/hashes/sha256";
import { bytesToHex } from "@noble/hashes/utils";
import { getOperationAST, Kind, Lexer, parse, print, Source, TokenKind, visit } from "graphql";
import type { FragmentDefinitionNode } from "graphql";

/** The selected operation and reachable fragments, independent of formatting. */
export function documentIdentity(query: string, operationName?: string): string {
  const key = JSON.stringify([query, operationName ?? null]);
  const cached = identities.get(key);
  if (cached) return cached;
  const identity = calculateIdentity(query, operationName);
  if (query.length <= 256 * 1024) {
    if (identities.size === 256) identities.delete(identities.keys().next().value!);
    identities.set(key, identity);
  }
  return identity;
}

const identities = new Map<string, string>();

function calculateIdentity(query: string, operationName?: string): string {
  const document = parse(query);
  const names = new Set<string>();
  for (const definition of document.definitions) {
    if (definition.kind !== Kind.OPERATION_DEFINITION) continue;
    const name = definition.name?.value ?? "";
    if (names.has(name)) throw new TypeError("duplicate operation");
    names.add(name);
  }
  const operation = getOperationAST(document, operationName);
  if (!operation) throw new TypeError("document must select exactly one GraphQL operation");
  const fragments = new Map<string, FragmentDefinitionNode>();
  for (const definition of document.definitions) {
    if (definition.kind !== Kind.FRAGMENT_DEFINITION) continue;
    if (fragments.has(definition.name.value)) throw new TypeError("duplicate fragment");
    fragments.set(definition.name.value, definition);
  }
  const used = new Set<string>();
  const active = new Set<string>();
  const collect = (node: typeof operation | FragmentDefinitionNode): void => {
    visit(node, {
      FragmentSpread(spread) {
        const name = spread.name.value;
        if (active.has(name)) throw new TypeError("cyclic fragment");
        if (used.has(name)) return;
        const fragment = fragments.get(name);
        if (!fragment) throw new TypeError(`missing fragment ${name}`);
        active.add(name);
        collect(fragment);
        active.delete(name);
        used.add(name);
      },
    });
  };
  collect(operation);
  const selected = print({
    kind: Kind.DOCUMENT,
    definitions: [operation, ...[...used].sort().map((name) => fragments.get(name)!)],
  });
  const lexer = new Lexer(new Source(selected));
  const encoder = new TextEncoder();
  let canonical = "fw-operation-v1;";
  let first = true;
  for (let token = lexer.advance(); token.kind !== TokenKind.EOF; token = lexer.advance()) {
    if (first && token.kind === TokenKind.BRACE_L) canonical += "Name:5:query;";
    first = false;
    const kind = token.kind === TokenKind.BLOCK_STRING ? TokenKind.STRING : token.kind;
    const value = token.value ?? "";
    canonical += `${kind}:${encoder.encode(value).length}:${value};`;
  }
  return `sha256:${bytesToHex(sha256(encoder.encode(canonical)))}`;
}
