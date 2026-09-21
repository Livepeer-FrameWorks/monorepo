/**
 * GraphQL subscriptions over WebSocket (graphql-transport-ws), built on the
 * optional peer dependency graphql-ws. This entry is the only one that
 * imports it.
 */
import type { DocumentTypeDecoration } from "@graphql-typed-document-node/core";
import { type Client, createClient as createWSClient } from "graphql-ws";

import {
  AuthenticationError,
  FrameWorksError,
  type GraphQLErrorEntry,
  NetworkError,
} from "./errors.js";
import { parseOperation } from "./client.js";
import { graphQLError, resolveToken, type TokenSource } from "./transport.js";

export interface SubscriptionClientOptions {
  /** The WebSocket endpoint, e.g. wss://bridge.example.com/graphql/ws. */
  url: string;
  /** Bearer token, or a function returning the current one; it is read again for every connection. */
  token?: TokenSource;
  /** WebSocket implementation; defaults to the global WebSocket (Node 22 and browsers). */
  webSocketImpl?: unknown;
  /**
   * Reconnects in a row without an acknowledged connection between them,
   * before the subscription fails (default 5). Each acknowledged connection
   * starts the count again.
   */
  maxReconnects?: number;
  /** Wait before reconnect number `retries` (0-based); defaults to randomized exponential backoff. */
  retryWait?: (retries: number) => Promise<void>;
}

export interface SubscriptionClient {
  /**
   * Subscribes and yields each event's data. The iterator ends when the
   * server completes the subscription and throws a typed error when it
   * fails: AuthenticationError when the server closes the connection with
   * 4403 before acknowledging it (an invalid or expired token), the mapped
   * GraphQL error for an error message, and NetworkError when reconnects run
   * out. Any other close or network error reconnects with backoff, except
   * the close codes graphql-ws treats as fatal (4004, 4005, 4400, 4401,
   * 4406, 4409, 4429, 4500, and 1002 to 1999 other than 1005, 1006, and
   * 1012 to 1014), which end the subscription with NetworkError.
   */
  subscribe<TResult, TVariables>(
    document: DocumentTypeDecoration<TResult, TVariables> | string,
    variables?: TVariables
  ): AsyncGenerator<TResult, void, undefined>;
  /** Closes the connection and ends every subscription. */
  close(): Promise<void>;
}

/** graphql-transport-ws 4403 Forbidden: the gateway rejected the Authorization value. */
const forbiddenCloseCode = 4403;

interface CloseLike {
  code: number;
  reason: string;
}

function isCloseLike(value: unknown): value is CloseLike {
  return value !== null && typeof value === "object" && "code" in value && "reason" in value;
}

interface Connection {
  client: Client;
  authError: AuthenticationError | null;
}

export function createSubscriptionClient(options: SubscriptionClientOptions): SubscriptionClient {
  let current: Connection | null = null;

  const connect = (): Connection => {
    if (current) {
      return current;
    }
    let opened = false;
    let acked = false;
    const connection: Connection = {
      authError: null,
      client: createWSClient({
        url: options.url,
        webSocketImpl: options.webSocketImpl,
        lazy: true,
        retryAttempts: options.maxReconnects ?? 5,
        retryWait: options.retryWait,
        shouldRetry: () => true,
        connectionParams: async () => {
          const token = await resolveToken(options.token);
          return token ? { Authorization: `Bearer ${token}` } : {};
        },
        on: {
          connecting: () => {
            opened = false;
            acked = false;
          },
          opened: () => {
            opened = true;
          },
          connected: () => {
            acked = true;
          },
          // The gateway closes a connection whose Authorization value does not
          // authenticate with 4403 before acknowledging it. Reconnecting with
          // the same token would loop, so the connection is disposed instead.
          // Any other close before the ack, such as 1013 when the gateway
          // could not check the token, reconnects.
          closed: (event) => {
            if (opened && !acked && isCloseLike(event) && event.code === forbiddenCloseCode) {
              connection.authError = new AuthenticationError(
                `subscription connection closed before it was acknowledged (${event.code} ${event.reason}); the token is invalid or expired`
              );
              if (current === connection) {
                current = null;
              }
              // dispose waits on the connection attempt, which rejects with this close.
              Promise.resolve(connection.client.dispose()).catch(() => undefined);
            }
          },
        },
      }),
    };
    current = connection;
    return connection;
  };

  return {
    async *subscribe<TResult, TVariables>(
      document: DocumentTypeDecoration<TResult, TVariables> | string,
      variables?: TVariables
    ): AsyncGenerator<TResult, void, undefined> {
      const query = String(document);
      const op = parseOperation(query);
      const connection = connect();
      const results = connection.client.iterate<TResult>({
        query,
        variables: (variables ?? {}) as Record<string, unknown>,
        ...(op.name ? { operationName: op.name } : {}),
      });
      try {
        for await (const result of results) {
          if (result.errors && result.errors.length > 0) {
            throw graphQLError(
              result.errors as GraphQLErrorEntry[],
              result.data ?? null,
              null,
              null,
              result
            );
          }
          yield result.data as TResult;
        }
      } catch (err) {
        if (connection.authError) {
          throw connection.authError;
        }
        if (err instanceof FrameWorksError) {
          throw err;
        }
        if (Array.isArray(err)) {
          throw graphQLError(err as GraphQLErrorEntry[], null, null, null, err);
        }
        if (isCloseLike(err)) {
          throw new NetworkError(`subscription connection closed (${err.code} ${err.reason})`, {
            cause: err,
          });
        }
        throw new NetworkError(`subscription connection failed: ${String(err)}`, { cause: err });
      } finally {
        await results.return?.(undefined);
      }
      if (connection.authError) {
        throw connection.authError;
      }
    },
    async close() {
      const connection = current;
      current = null;
      if (connection) {
        await Promise.resolve(connection.client.dispose()).catch(() => undefined);
      }
    },
  };
}
