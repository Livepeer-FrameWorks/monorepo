import assert from "node:assert/strict";
import { test } from "node:test";

import { footerColumns, headerNav, isActiveLink, signupUrl } from "./siteLinks.js";

const config = {
  docsUrl: "https://docs.example/",
  githubUrl: "https://github.com/example/repo",
};

test("header keeps every page and adds Showcase at the top level", () => {
  const labels = headerNav(config).map((item) => item.label);
  assert.deepEqual(labels, [
    "Home",
    "About",
    "Showcase",
    "Pricing",
    "Network",
    "Docs",
    "Contact",
    "Resources",
    "GitHub",
  ]);
});

test("the Resources menu lists the same links on desktop and mobile", () => {
  const resources = headerNav(config).find((item) => item.children);
  assert.deepEqual(
    resources.children.map((child) => child.label),
    ["Roadmap", "Stream test", "Status", "Skipper", "Blog"]
  );
  assert.equal(resources.children[0].href, "https://docs.example/roadmap");
  assert.equal(resources.children[4].href, "https://docs.example/blog");
});

test("footer groups links into product, company, resources and legal", () => {
  const columns = footerColumns(config);
  assert.deepEqual(
    columns.map((column) => column.title),
    ["Product", "Company", "Resources", "Legal"]
  );
  assert.deepEqual(
    columns[0].links.map((link) => link.label),
    ["Showcase", "Stream test", "Analytics", "Network", "Pricing"]
  );
  assert.deepEqual(
    columns[1].links.map((link) => link.label),
    ["About", "Contact"]
  );
});

test("a menu is active when one of its children is the current page", () => {
  const resources = headerNav(config).find((item) => item.children);
  assert.equal(isActiveLink(resources, "/stream-test"), true);
  assert.equal(isActiveLink(resources, "/pricing"), false);
});

test("signup links go to register with attribution and an optional plan", () => {
  assert.equal(
    signupUrl("https://app.example/", "pricing-developer", { plan: "developer" }),
    "https://app.example/register?utm_source=marketing&utm_content=pricing-developer&plan=developer"
  );
  assert.equal(
    signupUrl("https://app.example", "nav-try-now"),
    "https://app.example/register?utm_source=marketing&utm_content=nav-try-now"
  );
});
