/**
 * Typed selections over the whole public schema, for fields the generated
 * documents do not select. A selection object names the fields to return:
 *
 *   const select = createSelectClient(client);
 *   const { stream } = await select.query({
 *     stream: { __args: { id }, name: true, metrics: { status: true } },
 *   });
 *
 * `__args` passes arguments, `on_<Type>` selects a union member or interface
 * implementation, `__typename: true` returns the type name, and
 * `__scalar: true` every scalar field of that level that needs no argument.
 * A union or interface level always returns `__typename`, so its result can
 * be narrowed. A mutation or subscription names each root field it runs:
 * `__scalar` is refused at those roots. The selection is turned
 * into a document with Genql's operation builder and sent through the client
 * it wraps, so authentication, retries, typed errors, and the server version
 * check are those of client.request, and subscriptions run on a
 * SubscriptionClient from @livepeer-frameworks/api/subscriptions.
 *
 * This entry holds the schema's type map, so importing it adds that map to a
 * bundle; the main entry does not depend on it.
 */
import type { FrameWorksClient, RequestOptions } from "./client.js";
import { operations } from "./generated/manifest.js";
import {
  generateGraphqlOperation,
  type GraphqlOperation,
} from "./generated/select/runtime/generateGraphqlOperation.js";
import { linkTypeMap } from "./generated/select/runtime/linkTypeMap.js";
import type { LinkedType, LinkedTypeMap } from "./generated/select/runtime/types.js";
import type {
  Mutation,
  MutationGenqlSelection,
  Query,
  QueryGenqlSelection,
  Subscription,
  SubscriptionGenqlSelection,
} from "./generated/select/schema.js";
import types from "./generated/select/types.js";
import type { FieldsSelection } from "./select-types.js";
import type { SubscriptionClient } from "./subscriptions.js";

export * from "./generated/select/schema.js";
export type { FieldsSelection };

/** Names the operation (operationName) instead of sending it anonymously. */
export interface SelectionName {
  __name?: string;
}

/**
 * Keeps `__scalar` off a mutation or subscription root, where it would run
 * every root field that takes no required argument.
 */
export interface NoRootScalar {
  __scalar?: never;
}

export interface SelectClientOptions {
  /** Runs subscription selections; without it, subscription() throws. */
  subscriptions?: SubscriptionClient;
}

export interface SelectClient {
  /** Runs a query built from the selection and returns the selected fields. */
  query<R extends QueryGenqlSelection>(
    selection: R & SelectionName,
    options?: RequestOptions
  ): Promise<FieldsSelection<Query, R>>;
  /**
   * Runs a mutation built from the selection. Like client.request, it is sent
   * again only when the request provably never reached the server.
   */
  mutation<R extends MutationGenqlSelection & NoRootScalar>(
    selection: R & SelectionName,
    options?: RequestOptions
  ): Promise<FieldsSelection<Mutation, R>>;
  /** Subscribes with the selection and yields each event's selected fields. */
  subscription<R extends SubscriptionGenqlSelection & NoRootScalar>(
    selection: R & SelectionName
  ): AsyncGenerator<FieldsSelection<Subscription, R>, void, undefined>;
}

type OperationKind = "query" | "mutation" | "subscription";

// Linked on first use so importing the entry has no side effects.
let linked: LinkedTypeMap | undefined;

function root(kind: OperationKind): LinkedType {
  linked ??= linkTypeMap(types);
  const name = kind === "query" ? "Query" : kind === "mutation" ? "Mutation" : "Subscription";
  const type = linked[name];
  if (!type) {
    throw new TypeError(`the public schema has no ${name} type`);
  }
  return type;
}

/** Builds the document and variables a selection sends. */
export function buildOperation(
  kind: "query",
  selection: QueryGenqlSelection & SelectionName
): GraphqlOperation;
export function buildOperation(
  kind: "mutation",
  selection: MutationGenqlSelection & NoRootScalar & SelectionName
): GraphqlOperation;
export function buildOperation(
  kind: "subscription",
  selection: SubscriptionGenqlSelection & NoRootScalar & SelectionName
): GraphqlOperation;
export function buildOperation(kind: OperationKind, selection: SelectionName): GraphqlOperation {
  // The server version check reads an operation's release from the SDK
  // manifest by name, so a selection may not borrow a generated operation's.
  if (selection.__name && Object.hasOwn(operations, selection.__name)) {
    throw new TypeError(
      `__name ${selection.__name} is an SDK operation name; choose a name of your own`
    );
  }
  const type = root(kind);
  // At the Mutation root, `__scalar` would run every mutation that takes no
  // required argument (markSkipperReportsRead without ids marks every report
  // read); at the Subscription root it would open every such stream.
  if (kind !== "query" && "__scalar" in selection && selection.__scalar) {
    throw new TypeError(
      `${type.name}: __scalar is not allowed at the ${type.name} root; select each ${kind} field by name`
    );
  }
  checkSelection(type, selection, type.name);
  return generateGraphqlOperation(
    kind,
    type,
    withTypenames(type, selection) as Record<string, never>
  );
}

const selectionKeywords = new Set(["__args", "__name", "__scalar", "__typename"]);

// Unions and interfaces are the linked types with on_<Type> fields.
function isAbstract(type: LinkedType): boolean {
  return Object.keys(type.fields ?? {}).some((name) => name.startsWith("on_"));
}

// Returns a copy of the selection with `__typename: true` at every union and
// interface level, so each member's result carries the discriminant its
// FieldsSelection type declares.
function withTypenames(type: LinkedType, selection: object): object {
  const out: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(selection)) {
    const field = name === "__args" ? undefined : type.fields?.[name];
    out[name] =
      value && typeof value === "object" && field?.type.fields
        ? withTypenames(field.type, value as object)
        : value;
  }
  if (isAbstract(type)) {
    out.__typename = true;
  }
  return out;
}

// TypeScript does not reject unknown keys in an inferred generic selection,
// and Genql's builder copies a scalar field's name into the document without
// looking it up, so a misspelt field would reach the server and fail there as
// a SchemaMismatchError. Every selected name is checked against the schema
// before anything is sent.
function checkSelection(type: LinkedType, selection: object, path: string): void {
  for (const [name, value] of Object.entries(selection)) {
    if (!value || selectionKeywords.has(name)) {
      continue;
    }
    const field = type.fields?.[name];
    if (!field) {
      throw new TypeError(`${path}: ${type.name} has no field ${name}`);
    }
    if (typeof value === "object" && field.type.fields) {
      checkSelection(field.type, value as object, `${path}.${name}`);
    }
  }
}

export function createSelectClient(
  client: FrameWorksClient,
  options: SelectClientOptions = {}
): SelectClient {
  // A selection the builder refuses rejects the returned promise.
  const run = async <T>(
    build: () => GraphqlOperation,
    requestOptions: RequestOptions = {}
  ): Promise<T> => {
    const op = build();
    return client.request<T, Record<string, unknown>>(op.query, op.variables ?? {}, requestOptions);
  };
  return {
    query: (selection, requestOptions) =>
      run(() => buildOperation("query", selection), requestOptions),
    mutation: (selection, requestOptions) =>
      run(() => buildOperation("mutation", selection), requestOptions),
    async *subscription<R extends SubscriptionGenqlSelection & NoRootScalar>(
      selection: R & SelectionName
    ): AsyncGenerator<FieldsSelection<Subscription, R>, void, undefined> {
      if (!options.subscriptions) {
        throw new TypeError(
          "createSelectClient: pass a SubscriptionClient from @livepeer-frameworks/api/subscriptions to run subscriptions"
        );
      }
      const op = buildOperation("subscription", selection);
      yield* options.subscriptions.subscribe<
        FieldsSelection<Subscription, R>,
        Record<string, unknown>
      >(op.query, op.variables ?? {});
    },
  };
}
