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
 * `__scalar: true` every scalar field of that level. The selection is turned
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
import type { FieldsSelection } from "./generated/select/runtime/typeSelection.js";
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
import type { SubscriptionClient } from "./subscriptions.js";

export * from "./generated/select/schema.js";
export type { FieldsSelection };

/** Names the operation (operationName) instead of sending it anonymously. */
export interface SelectionName {
  __name?: string;
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
  mutation<R extends MutationGenqlSelection>(
    selection: R & SelectionName,
    options?: RequestOptions
  ): Promise<FieldsSelection<Mutation, R>>;
  /** Subscribes with the selection and yields each event's selected fields. */
  subscription<R extends SubscriptionGenqlSelection>(
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
  kind: OperationKind,
  selection: (QueryGenqlSelection | MutationGenqlSelection | SubscriptionGenqlSelection) &
    SelectionName
): GraphqlOperation {
  // The server version check reads an operation's release from the SDK
  // manifest by name, so a selection may not borrow a generated operation's.
  if (selection.__name && Object.hasOwn(operations, selection.__name)) {
    throw new TypeError(
      `__name ${selection.__name} is an SDK operation name; choose a name of your own`
    );
  }
  const type = root(kind);
  checkSelection(type, selection, type.name);
  return generateGraphqlOperation(kind, type, selection as Record<string, never>);
}

const selectionKeywords = new Set(["__args", "__name", "__scalar", "__typename"]);

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
  const run = <T>(
    kind: "query" | "mutation",
    selection: QueryGenqlSelection | MutationGenqlSelection,
    requestOptions: RequestOptions = {}
  ): Promise<T> => {
    const op = buildOperation(kind, selection);
    return client.request<T, Record<string, unknown>>(op.query, op.variables ?? {}, requestOptions);
  };
  return {
    query: (selection, requestOptions) => run("query", selection, requestOptions),
    mutation: (selection, requestOptions) => run("mutation", selection, requestOptions),
    async *subscription<R extends SubscriptionGenqlSelection>(
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
