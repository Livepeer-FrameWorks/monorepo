import { ProtocolError, ResultError } from "./errors.js";

/**
 * Returns a result union member when its __typename is one of success, and
 * throws ResultError for any other member (ValidationError, NotFoundError,
 * AuthError, RateLimitError, or a member this SDK does not know yet).
 *
 *     const stream = expectResult(data.createStream, "Stream");
 */
export function expectResult<T extends { __typename: string }, K extends T["__typename"]>(
  value: T | null | undefined,
  ...success: K[]
): Extract<T, { __typename: K }> {
  if (value === null || value === undefined) {
    throw new ProtocolError("result is missing");
  }
  if ((success as string[]).includes(value.__typename)) {
    return value as Extract<T, { __typename: K }>;
  }
  throw new ResultError(value as unknown as Record<string, unknown>);
}
