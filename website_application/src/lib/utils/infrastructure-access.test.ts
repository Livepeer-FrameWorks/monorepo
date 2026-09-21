import { describe, expect, it } from "vitest";
import { hasInfrastructureOperatorRole } from "./infrastructure-access";

describe("hasInfrastructureOperatorRole", () => {
  it.each(["owner", "admin", " ADMIN "])("allows %s", (role) => {
    expect(hasInfrastructureOperatorRole({ role })).toBe(true);
  });

  it.each([undefined, "", "member", "viewer"])("denies %s", (role) => {
    expect(hasInfrastructureOperatorRole({ role })).toBe(false);
  });

  it("allows platform operators independently of tenant role", () => {
    expect(hasInfrastructureOperatorRole({ role: "member", platform_operator: true })).toBe(true);
  });

  it("follows the server capabilities reading over the token claim", () => {
    expect(hasInfrastructureOperatorRole({ role: "member", platform_operator: false }, true)).toBe(
      true
    );
    expect(hasInfrastructureOperatorRole({ role: "member", platform_operator: true }, false)).toBe(
      false
    );
  });

  it("falls back to the token claim while the reading is unknown", () => {
    expect(hasInfrastructureOperatorRole({ role: "member", platform_operator: true }, null)).toBe(
      true
    );
    expect(hasInfrastructureOperatorRole({ role: "member" }, null)).toBe(false);
  });

  it("keeps tenant owners and admins when the tenant is not an operator", () => {
    expect(hasInfrastructureOperatorRole({ role: "owner" }, false)).toBe(true);
  });
});
