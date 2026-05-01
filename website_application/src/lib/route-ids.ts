export interface StreamIdentifierInput {
  routeParamId: string;
  streamUuid?: string | null;
}

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUuid(value: string | null | undefined): value is string {
  return typeof value === "string" && UUID_REGEX.test(value);
}

export function resolveOperationalStreamId({
  routeParamId,
  streamUuid,
}: StreamIdentifierInput): string {
  if (isUuid(streamUuid)) {
    return streamUuid;
  }

  if (isUuid(routeParamId)) {
    return routeParamId;
  }

  return "";
}
