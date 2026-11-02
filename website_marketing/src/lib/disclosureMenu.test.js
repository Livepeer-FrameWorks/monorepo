import assert from "node:assert/strict";
import { test } from "node:test";

import { nextMenuOpen } from "./disclosureMenu.js";

const run = (actions, open = false) => actions.reduce(nextMenuOpen, open);

test("hovering then clicking with a mouse leaves the menu open", () => {
  assert.equal(
    run([
      { type: "hover", pointerType: "mouse" },
      { type: "click", pointerType: "mouse" },
    ]),
    true
  );
});

test("keyboard and touch clicks toggle", () => {
  assert.equal(run([{ type: "click", pointerType: "" }]), true);
  assert.equal(run([{ type: "click", pointerType: "" }], true), false);
  assert.equal(run([{ type: "click", pointerType: "touch" }], true), false);
});

test("touch pointer enter does not open the menu", () => {
  assert.equal(run([{ type: "hover", pointerType: "touch" }]), false);
});

test("mouse leave and dismiss close the menu", () => {
  assert.equal(run([{ type: "leave", pointerType: "mouse" }], true), false);
  assert.equal(run([{ type: "dismiss" }], true), false);
});
