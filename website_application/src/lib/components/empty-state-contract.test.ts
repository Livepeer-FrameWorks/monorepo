import { describe, expect, it, expectTypeOf } from "vitest";
import {
  resolveEmptyStateIcon,
  type EmptyStateContract,
  type EmptyStateIconProps,
} from "./empty-state-contract";

describe("resolveEmptyStateIcon", () => {
  it("keeps the previous iconName contract", () => {
    expect(resolveEmptyStateIcon({ iconName: "Users" })).toBe("Users");
  });

  it("supports icon alias for API consistency", () => {
    expect(resolveEmptyStateIcon({ icon: "Globe" })).toBe("Globe");
  });

  it("prefers iconName when both props are present", () => {
    expect(resolveEmptyStateIcon({ icon: "Globe", iconName: "Users" })).toBe("Users");
  });

  it("falls back to FileText when icon props are missing", () => {
    expect(resolveEmptyStateIcon({})).toBe("FileText");
  });
});

describe("EmptyState contract types", () => {
  it("keeps icon props constrained to known icon names", () => {
    expectTypeOf<EmptyStateIconProps>().toMatchTypeOf<{ icon?: string; iconName?: string }>();
  });

  it("keeps buttonVariant aligned with Button variant API", () => {
    expectTypeOf<EmptyStateContract>().toMatchTypeOf<{ buttonVariant?: string }>();
  });
});
