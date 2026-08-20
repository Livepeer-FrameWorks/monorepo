export function safeReturnTo(value: string | null, loginPath: string): string | null {
  if (!value || !value.startsWith("/") || value.startsWith("//")) return null;
  if (value === loginPath || value.startsWith(`${loginPath}?`)) return null;
  return value;
}
