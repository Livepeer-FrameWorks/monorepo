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
});
