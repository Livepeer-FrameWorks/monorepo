import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const generated = readFileSync(new URL("../src/generated/graphql.ts", import.meta.url), "utf8");

describe("generated schema documentation", () => {
  it("documents selected result types and fields", () => {
    expect(generated).toContain(
      "/** A live stream configuration with real-time operational metrics. Streams are the core entity for broadcasting and viewing live content. */\nexport type StreamFieldsFragment"
    );
    expect(generated).toMatch(
      /\/\*\* Human-readable display name for the stream\. \*\/\nname: string/
    );
  });

  it("documents operation result types and documents", () => {
    const description = "/** Create a new stream for live broadcasting. */";
    expect(generated).toContain(`${description}\nexport type CreateStreamMutation`);
    expect(generated).toContain(`${description}\nexport const CreateStreamDocument`);
  });

  it("marks exactly the operations on @experimental fields as experimental", () => {
    const targets = JSON.parse(
      readFileSync(
        new URL("../../pkg/graphql/public/generated/targets.json", import.meta.url),
        "utf8"
      )
    ) as { experimental: Record<string, { note: string }> };
    const documented = [
      ...generated.matchAll(/\/\*\* ([^\n]*) \*\/\nexport const (\w+)Document\b/g),
    ].map(([, comment = "", name = ""]) => ({ comment, name }));
    const marked = documented
      .filter(({ comment }) => comment.includes("Experimental until"))
      .map(({ name }) => name)
      .sort();
    expect(marked).toEqual(Object.keys(targets.experimental).sort());
    for (const { comment, name } of documented) {
      const note = targets.experimental[name]?.note;
      if (note) expect(comment.endsWith(note)).toBe(true);
    }
  });
});
