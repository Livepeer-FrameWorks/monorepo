import assert from "node:assert/strict";
import { test } from "node:test";

import { customerStories, customerStoryPath, findCustomerStory } from "./customerStories.js";

const story = { slug: "example", customer: "Example" };

test("finds a story by its slug", () => {
  assert.equal(findCustomerStory("example", [story]), story);
});

test("returns nothing for an unknown slug", () => {
  assert.equal(findCustomerStory("missing", [story]), undefined);
});

test("builds the unlisted story path", () => {
  assert.equal(customerStoryPath(story), "/customers/example");
});

test("every published story has a unique slug and the fields the page renders", () => {
  assert.equal(new Set(customerStories.map(({ slug }) => slug)).size, customerStories.length);
  for (const published of customerStories) {
    for (const field of ["customer", "headline", "summary"]) {
      assert.ok(published[field], `${published.slug} is missing ${field}`);
    }
    for (const field of ["situation", "approach", "setup", "results"]) {
      assert.ok(published[field]?.length, `${published.slug} is missing ${field}`);
    }
  }
});
