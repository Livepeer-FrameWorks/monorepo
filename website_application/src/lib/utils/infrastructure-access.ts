export function hasInfrastructureOperatorRole(
  user: { role?: string; platform_operator?: boolean } | null | undefined
): boolean {
  if (user?.platform_operator) return true;
  const role = user?.role?.trim().toLowerCase();
  return role === "owner" || role === "admin";
}
