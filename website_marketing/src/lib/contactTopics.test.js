import assert from "node:assert/strict";
import { test } from "node:test";

import {
  CONTACT_TOPICS,
  DEFAULT_CONTACT_TOPIC,
  contactPath,
  contactTopic,
} from "./contactTopics.js";

test("the site's contact links all name a known topic", () => {
  for (const id of ["engineering", "trial", "pricing", "channel"]) {
    assert.equal(contactTopic(id).id, id);
  }
});

test("a missing or unknown topic falls back to the default", () => {
  assert.equal(contactTopic(null).id, DEFAULT_CONTACT_TOPIC);
  assert.equal(contactTopic("sales").id, DEFAULT_CONTACT_TOPIC);
});

test("every topic has the copy the form and hero render", () => {
  assert.equal(new Set(CONTACT_TOPICS.map((topic) => topic.id)).size, CONTACT_TOPICS.length);
  for (const topic of CONTACT_TOPICS) {
    for (const field of ["label", "title", "description"]) {
      assert.ok(topic[field], `${topic.id} is missing ${field}`);
    }
  }
});

test("contact paths carry the topic", () => {
  assert.equal(contactPath("trial"), "/contact?topic=trial");
});
