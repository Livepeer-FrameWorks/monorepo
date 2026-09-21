/**
 * Whether the signed-in user may see the infrastructure operator surfaces.
 * platformOperator is the server's capabilities reading; null means it has not
 * arrived yet, and the signed token's own claim answers instead. Tenant owners
 * and admins keep access through their role either way.
 */
export function hasInfrastructureOperatorRole(
  user: { role?: string; platform_operator?: boolean } | null | undefined,
  platformOperator?: boolean | null
): boolean {
  if ((platformOperator ?? user?.platform_operator ?? false) === true) return true;
  const role = user?.role?.trim().toLowerCase();
  return role === "owner" || role === "admin";
}
