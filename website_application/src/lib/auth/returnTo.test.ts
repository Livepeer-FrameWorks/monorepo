import { describe, expect, it } from "vitest";
import { safeReturnTo } from "./returnTo";

describe("safeReturnTo", () => {
  it("accepts same-origin absolute paths including query strings", () => {
    expect(safeReturnTo("/account/balance?method=crypto", "/login")).toBe(
      "/account/balance?method=crypto"
    );
  });

  it.each([null, "", "https://evil.example", "//evil.example", "/login", "/login?next=/"])(
    "rejects unsafe or recursive target %s",
    (value) => {
      expect(safeReturnTo(value, "/login")).toBeNull();
    }
  );
});
