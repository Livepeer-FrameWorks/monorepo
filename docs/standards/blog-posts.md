# Blog Posts

How we write posts in `website_docs/src/content/docs/blog/`. A post explains why a change matters to the people who
use FrameWorks. The docs explain how to use it. When a draft starts listing fields, flags, enum values, timeouts or
status codes, that material belongs in the docs page the post links to.

## When to write one

- **Yes:** a customer-facing capability that changes what someone can do or what it costs them, an architecture decision
  readers will ask about, or an incident or audit with findings worth sharing.
- **No:** a defect fix that makes an existing capability work as promised, hardening that users will not notice, or a
  feature with no scenario to tell and nothing measured. Those get a line in the release notes
  ([release-notes.md](release-notes.md)) and, where it applies, a docs update.

A feature is not done without its `docs/platform-features.yaml` entry and docs page. The post is part of shipping it
only when it passes this standard; if it cannot, say so and put it in the release notes instead.

## Shape

The best posts so far (`agentic-audit-pipeline`, `public-gitops`, `webhooks-and-public-events`, `studio-parity`)
share this shape:

1. **Open on the reader's situation.** The first paragraph is a before-state ("Until now…"), a real event ("Last week
   we merged…"), or a question a customer asked. Never a definition, and never an internal service name.
2. **Say what changed, briefly.** One or two sentences a reader can repeat.
3. **Show one concrete case.** Walk through a scenario the reader recognizes: a broadcaster in the EU with viewers in
   the US, one stream restreamed to YouTube and to a bitrate-capped destination, a backend that restarts during a
   deploy.
4. **Bring evidence.** A measured number (latency, CPU, cost, error rate), a before/after, a real request or bug, or
   audit counts. Numbers are evidence, not configuration.
5. **State the cost or limit once.** One short section on what it does not do or what it costs, stated plainly. Not a
   pile of caveats in place of a point.
6. **End with the docs link.** Link the page that holds the knobs and field-level detail.

Length is 600 to 950 words. Use three to five H2 headings phrased as claims or questions ("What it costs", "Delivery
survives the failure it reports"), not component names.

## Keep out

- Lists of enum values, field names, flags, env vars, ports, status codes, retry schedules, timeouts.
- Our internal architecture. Posts can be technical about protocols, codecs, latency, trust and failure models, and the
  trade-offs behind a decision, but not about how our services are wired. No internal service names (Foghorn,
  Helmsman, Commodore, Periscope and so on), Mist trigger names, RPCs, tables, outboxes or leases. Say what the reader
  gets: "viewers who are already watching keep watching when the control plane is briefly unreachable", not "bounded
  Helmsman recovery for previously approved play rewrites". Product names readers use (MistServer, the CLI, Skipper
  as a product) are fine.
- Step-by-step setup. Link the guide.
- Claims about behavior you have not checked against the code or docs, and claims that contradict an earlier post.
  When behavior changed since an earlier post, say so and link it.
- Stock phrasing: "isn't just X, it's Y", "game-changer", punchline endings, systems described as wanting or deciding
  things. Specific detail carries the tone.

## Frontmatter

```yaml
---
title: "Plain statement of what changed"
date: YYYY-MM-DD
authors:
  - frameworks
excerpt: "One or two sentences on the change and why it matters."
description: "Same as excerpt unless search needs different wording."
tags:
  - announcements
---
```

Reuse existing tags: `announcements`, `engineering`, `architecture`, `releases`, `ai`, `federation`, `operations`,
`playback`, `edge`, `observability`, `payments`, `sovereignty`, `security`, `dvr`. Add a new tag only when no
existing one fits.

## Checklist

Before a post ships:

1. The first paragraph names a situation or event, not a component.
2. There is one concrete scenario, and a measured number or real example where one exists.
3. Every field, flag or limit mentioned is one the reader needs to understand the change; the rest is in the linked docs.
4. Every behavior claim matches the current code and docs, and no earlier post says otherwise.
5. The last paragraph links the docs.
6. It reads within 600 to 950 words.
