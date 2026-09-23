# GraphQL Public Contract and SDK Compatibility

The public GraphQL schema is the API contract. `sdkcontract` (`scripts/sdkcontract`) derives it from
`pkg/graphql/schema.graphql`, generates the SDK operations and the API reference from it, and checks every change
against the released schema. This document is the policy for changing it; the gates in
[Gates](#gates) enforce it.

## The public schema

Visibility is declared on field definitions in `pkg/graphql/schema.graphql` with two metadata directives. The gateway
skips both at runtime (`skip_runtime` in `api_gateway/gqlgen.yml`).

- **`@internal(reason: String!)`** marks a field, root or nested, that a tenant credential cannot use: operator-only
  and service-token-only fields. `make generate-public-schema` writes `pkg/graphql/public/schema.public.graphql`, the
  schema without its `@internal` fields and without the types only those fields reach. A public field whose
  description says "service token required" or that every field requires the `platform_operator` grant fails
  generation until it is marked `@internal`.
- **`@experimental(reason: String!, until: String!)`** marks a public field that is not stable yet. `until` is the
  platform release (`vMAJOR.MINOR.PATCH`) by which the field either loses the directive or is removed. Experimental
  fields are in the public schema, the SDKs, and the reference (badged **Experimental until vX.Y.Z**); they are exempt
  from the schema compatibility check. An SDK operation whose target path has an experimental field (the root or any
  field on the path) is an experimental operation: its doc comment in all three SDKs and its row in the reference's
  **SDK** table say so, and it is exempt from the operation rules of a line (see [What may change](#what-may-change)).
  `make verify-experimental-fields` lists every experimental field and fails once the pending release in
  `cli/internal/releases/catalog.yaml` reaches a field's `until`.

Everything else in the public schema is stable. The SDK generators (genqlient, graphql-codegen, Genql,
ariadne-codegen), the operation lint, the compatibility checks, and the reference read `schema.public.graphql`, never
the full schema, so no SDK exposes an `@internal` field. The webapp (Houdini) and the macOS tray keep reading the full
schema.

## SDK operations

The TypeScript, Go, and Python SDKs are generated from the operation documents in `pkg/graphql/public`. There are two
kinds.

**Default operations.** `make generate-ops` writes `pkg/graphql/public/generated/{queries,mutations,subscriptions,
fragments}.graphql` from the public schema (`scripts/sdkcontract/defaultops.go` holds the rules):

- **Targets.** Every public, non-deprecated field of `Query`, `Mutation`, and `Subscription`, and every non-deprecated
  field that takes arguments on a type reached from `Query` through singular (non-list) object fields. A type is
  visited once, at its shortest distance from `Query` (schema order breaks ties), so each such field has one path. The
  operation selects the path from the root to the field, and every argument on the path is a variable: the target
  keeps its argument names, and an ancestor's argument that clashes becomes `<field><Arg>`. Variables with a schema
  default keep it. An argument field on a type that walk does not reach but that implements `Node` is a target through
  `Query.node`: the operation selects `node(id: $id) { __typename ... on <Type> { <field>(...) { ... } } }`, and the
  target is keyed with the type after `node` (`query.node.InfrastructureNode.metricsConnection`). Other argument fields
  reached only through lists, unions, or interfaces, and paths below `Mutation` and `Subscription`, are not targets
  (`make sdk-audit` lists them; there are none today). A target whose default selection is empty (a namespace such as
  `Query.analytics`) gets no operation; its argument fields do.
- **Selection.** A per-type fragment `<Type>DefaultFields`: the type's scalar and enum fields and its argument-free
  object, union, and interface fields to depth 2. Connections select `edges { cursor node }`, `pageInfo`, and
  `totalCount`. Unions and interfaces select `__typename` and each member's fragment; a member field whose type
  differs from the other members' is aliased `<member><Field>`. Fields that take arguments, deprecated fields, and
  `@experimental` fields are never part of a selection, so no operation with a stable target selects a field that may
  be removed within the line. An experimental field that is a root or takes arguments is reached through its own
  experimental operation; an argument-free experimental field below a root has no generated operation and is
  selected through the TypeScript select client or a custom document.
- **Names.** A query is `Get<Field>`, a mutation or subscription `<Field>`, in PascalCase. A nested query is
  `Get<Field>`, or `Get` followed by every field of its path when another query target ends in a field of the same
  name. A query through `Query.node` is `Get<Type><Field>` (`GetInfrastructureNodeMetricsConnection`). A name equal
  to a public schema type gets the operation kind as suffix.

**Hand-written operations** in `pkg/graphql/public/{queries,mutations,subscriptions}` (with shared fragments in
`fragments/`) are overrides. An operation's target is the path its `# @target` comment names, directly above the
operation:

```graphql
# @target query.stream.pushTargets
query ListPushTargets($streamId: ID!) {
  stream(id: $streamId) {
    id
    pushTargets {
      ...PushTargetFields
    }
  }
}
```

Without the annotation, the target is the operation's single root field, followed down while the selection below the
current field holds exactly one field with a selection of its own (`stream(id:) { pushTargets { ... } }` targets
`query.stream.pushTargets`; adding `id` beside `pushTargets` would make it `query.stream`). A path is
`kind.field.field`, with a member type name after a field of union or interface type. The loader
(`scripts/sdkcontract/ops.go`) fails when an annotation names a field the public schema lacks, a path the operation
does not select, or a kind other than the operation's; when two documents declare the same operation name; or when two
operations address the same target. The generator emits nothing for a target an override already addresses, and
annotates its own `Query.node` operations. genqlient copies the comment into the Go doc comment, and
`sdk_go/tools/genclient` removes it there; graphql-codegen and ariadne-codegen drop comments.

`make generate-ops` also writes `generated/targets.json`, every operation's target, and under `experimental` each
experimental operation with its field's `until`, `reason`, and the `note` the SDKs print ("Experimental until vX.Y.Z:
<reason> A later SDK release of this line may change or remove this operation."). The SDK code generators read it to
document each method with its target field's description and, for an experimental operation, that note, so none of
them re-derives a target. The manifest records the same `until` as each experimental operation's `experimental`.

The operation lint holds every document, generated or hand-written, to the public schema: it validates, selects
`__typename` and every error member in each union selection, uses no deprecated field, argument, or enum value, and
selects an `@experimental` field only when its own target is experimental (a hand-written override with a stable
target that selects one fails).
`make sdk-audit` fails when a target has no operation. `make sdk-manifest` depends on `generate-ops`, so the manifest
is never built from a stale set.

So each SDK has a typed method for every non-deprecated public root field (a namespace root through its argument
fields) and every non-deprecated field that takes arguments, apart from the list-only fields `make sdk-audit` reports
(none today). Argument-free fields nested deeper than the default selection are typed only through the TypeScript
select client.

**Custom selections** beyond the default selection:

- **TypeScript:** `@livepeer-frameworks/api/select`, a Genql client generated from the public schema that sends
  selections through the SDK client (authentication, retries, typed errors, server check). `client.request` also
  takes a document string.
- **Go:** callers generate their own genqlient functions against `schema.public.graphql` and run them through
  `*frameworks.Client`, which implements `graphql.Client` (`MakeRequest`).
- **Python:** no typed builder. ariadne-codegen's custom operation builder has no subscriptions and returns untyped
  dictionaries with undecoded scalars; custom documents go through `execute` and the async client's `execute_ws`.

A custom operation needs a name of its own: the server check looks up an SDK operation's `since` by name.

## SDK lines

`pkg/graphql/public/support.yaml` lists the SDK lines and the platform releases each supports.

- **Line.** Under 0.x, a minor version is its own line (0.1, 0.2, 0.3, ...), because a 0.x minor release may break.
  From 1.0 on, a major version is a line.
- **Live lines.** A line is `live` or `retired` as declared in `support.yaml`. Live lines are checked; `current` is
  the line the working tree builds.
- **Minimum server.** Each line names `min_server`, a stable release. A live line supports every stable release at or
  above it. Release candidates and development builds are not supported releases: the SDKs treat their version as
  unverified and skip every version check.
- **One version for three languages.** The SDKs share `npm_api/package.json`'s version, `support.yaml`, and the
  operation manifest. `pnpm version-packages` applies the changesets and runs `make sdk-version-sync`, which copies the
  version into the Go and Python packages.

`pkg/graphql/public/majors/v<MAJOR>.json` holds, per line, every operation document with its hash and `since`, the
first release that serves it. `make sdk-manifest` rewrites the current line's entry; the entries of other lines are
frozen at what they shipped.

## What may change

**Operations, within a live line:**

- Add an operation (a new public field gets its default operation on the next `make graphql-sdk`). Its `since` may be
  above the line's minimum server: the SDKs check the server before sending it and raise `UnsupportedOperationError`
  against an older release.
- Add a field to an existing operation only if the field exists in the line's minimum server; otherwise the
  operation's `since` rises, which is not allowed.
- Never remove an operation, and never raise an operation's `since`. `make verify-api-compat` compares the manifest
  with the one tagged `sdk-v<line>.<patch>` at the latest release of the line, and validates every frozen live line's
  documents on every release from its minimum server.
- Exception: an operation the released manifest marks `experimental` (its target path had an `@experimental` field)
  may be removed or have its `since` raised within the line, and a frozen line's experimental operation may stop
  validating. Its field can be removed by its `until` release, and the operation goes with it. A stable field's
  operations cannot become experimental, because the field cannot (see below).

**The public schema.** The contract is everything a client can reach from the released public schema's root types,
whether or not an SDK operation selects it. `make verify-schema-compat` (`scripts/sdkcontract/schemadiff.go`) walks
the latest release's public schema from its roots, without crossing fields that were `@experimental` in that release,
and reports as breaking:

- a removed type, field, argument, input field, enum value, union member, or interface implementation;
- a type whose kind changed (for example object to interface);
- a field or argument whose named type or list nesting changed;
- an output position that was non-null becoming nullable;
- an input position (argument or input field) that was nullable becoming non-null, or a non-null one losing its
  default;
- an argument or input field whose default value changed or was removed, nullable or not: a request that omits it
  would then mean something else (for example `MediaPlacementOptionsFilter.spillover` moving off `NEVER`). Defaults are
  compared as normalized literals, so respelling one (a block string, reordered input object fields) is not a change.
  Adding a default, including to a previously required input, is compatible;
- a new required argument or required input field without a default;
- an enum value removed in either position: as input a client sending it is rejected, as output the regenerated SDK
  of the same line drops the constant, so consumer code that names it stops compiling;
- a union member or interface implementation removed: fragments on it inside selections of the union or interface
  stop validating;
- a stable field becoming `@experimental`, which would otherwise let a field leave the contract and then be removed.

Exempt: fields `@experimental` in the released schema and whatever only they reach, and any element `@deprecated` in
the tagged minimum-server schema of every live line. Additions (including new `@experimental` fields), new enum
values, new union members, an output position becoming non-null, and an input position becoming nullable are
compatible and printed for release notes. Every live SDK decodes new enum values and union members: an unknown union
member becomes the SDK's unknown member carrying its `__typename` and raw JSON (TypeScript returns the object as sent,
Go `*UnknownMember`, Python `UnknownMember`), an unknown enum value keeps the server's string (TypeScript `%future
added value`, Go string enums, Python `OpenEnum`), and `expectResult` turns an unknown member into a `ResultError`
carrying its `__typename`, message, and code. `sdk_conformance/forward_compat.json` holds all three languages to this.
Call out a new value on a type SDK users branch on in the release notes, since that code only reaches its default
case.

**Baseline.** The contract starts at the oldest `min_server` of the live lines in `support.yaml` (v0.3.11). While the
latest release tag is below it, `verify-schema-compat` prints the diff and passes; from that tag on, a breaking
change fails. To remove or incompatibly change a stable element, deprecate it first and remove it once the minimum
server of every live line has it deprecated.

## Breaking a line

To remove an operation, change one incompatibly, or raise the minimum server, start a new line before changing the
operations, so the previous line's frozen manifest entry is what it shipped:

1. Add a changeset with a minor bump (under 0.x) or a major bump (from 1.0) for `@livepeer-frameworks/api`.
2. Add the new line to `support.yaml` as `current`, with its `min_server`. Mark an older line `retired` only when its
   support commitment ends.
3. Run `pnpm version-packages` and `make graphql-sdk`: the version moves to the new line in all three SDKs,
   `make sdk-manifest` writes the new line's entry, and the previous line's entry stays frozen.
4. Deprecate the schema elements the new line stops using. They can be removed once the minimum server of every live
   line already has them deprecated.

## Gates

`make sdk-release-gates` runs all of them, and `scripts/publish-packages.sh` runs it before any language publishes:

| Gate                                                            | Checks                                                                                  |
| --------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `make test-sdkcontract`                                         | the contract tool's tests, then `sdk-audit` (every target has exactly one operation)    |
| `make verify-public-schema`                                     | `schema.public.graphql` matches `schema.graphql`                                        |
| `make verify-ops`                                               | `pkg/graphql/public/generated/` matches the public schema and the overrides             |
| `make verify-graphql-reference`                                 | `website_docs/.../builders/api-schema/` matches the public schema and the operations    |
| `make verify-experimental-fields`                               | no `@experimental` field is at or past its `until` release                              |
| `make verify-api-compat`                                        | the manifest is current, no stable operation removed or `since` raised within a line    |
| `make verify-schema-compat`                                     | no breaking public-schema change since the latest release (report-only before baseline) |
| `make verify-sdk-generated`                                     | the generated SDK code, manifest, and fixtures match a fresh `make graphql-sdk`         |
| `test-sdk-ts`, `test-sdk-go`, `test-sdk-py`, `typecheck-sdk-py` | each SDK's tests, examples, and type checks                                             |

CI runs the same gates in the `sdk` and `generated-contracts` jobs of `.github/workflows/ci.yml`.

## Error codes and event payloads

The `extensions.code` values the gateway sets (`UNAUTHORIZED`, `FORBIDDEN`, `NOT_FOUND`, `VALIDATION_ERROR`,
`CONFLICT`, `FAILED_PRECONDITION`, `RATE_LIMITED`, `UNAVAILABLE`, `INTERNAL_ERROR`) are part of the contract: codes are
added, never renamed. Public event payloads, which the `tenantEvents` subscription and webhooks deliver, follow the
protobuf rules in `pkg/proto/events/public/v1`: `make proto-breaking` checks them against the latest release at the
file level.
