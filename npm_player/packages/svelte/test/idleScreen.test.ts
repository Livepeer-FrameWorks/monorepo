import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("Svelte IdleScreen diagnostics", () => {
  it("supports separate user-facing error text and diagnostic details", () => {
    const idleScreenPath = path.resolve(__dirname, "../src/IdleScreen.svelte");
    const source = fs.readFileSync(idleScreenPath, "utf8");

    expect(source).toContain("details?: string");
    expect(source).toContain("let displayDetails = $derived");
    expect(source).toContain('<div class="status-details">{displayDetails}</div>');
  });
});
