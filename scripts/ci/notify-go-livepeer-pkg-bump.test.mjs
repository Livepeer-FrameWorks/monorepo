import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const workflow = readFileSync(
  new URL("../../.github/workflows/notify-go-livepeer-pkg-bump.yml", import.meta.url),
  "utf8"
);
const payloadTemplate = workflow.match(/client-payload: \|\n([\s\S]+)$/)?.[1];

for (const message of [
  "fix(media): preserve artifact authority",
  "fix(media): preserve artifact authority\n\nKeep the recording owner.\n",
  'fix: preserve "quoted" paths C:\\media\\clip\tand unicode 🎬',
]) {
  test(`dispatch preserves commit message ${JSON.stringify(message)}`, () => {
    assert.ok(payloadTemplate, "workflow must declare its dispatch payload");
    const context = {
      "github.sha": "9e739479cee81aa68659ffe0e5c0db14118b108b",
      "github.ref": "refs/heads/master",
      "github.event.head_commit.message": message,
    };
    // Render the workflow's expressions before applying the dispatch action's JSON parser.
    const rendered = payloadTemplate.replace(/\$\{\{\s*(.*?)\s*\}\}/g, (_match, expression) => {
      const encoded = expression.match(/^toJSON\((.*?)\)$/);
      const key = encoded ? encoded[1] : expression;
      assert.ok(Object.hasOwn(context, key), `unexpected payload expression: ${expression}`);
      return encoded ? JSON.stringify(context[key]) : context[key];
    });
    assert.deepEqual(JSON.parse(rendered), {
      monorepo_sha: context["github.sha"],
      monorepo_short_sha: context["github.sha"],
      monorepo_ref: context["github.ref"],
      triggered_by: message,
    });
  });
}
