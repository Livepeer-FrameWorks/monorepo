---
"@livepeer-frameworks/api": patch
---

Add typed camelCase operation methods through `@livepeer-frameworks/api/operations`.
Bind operation-specific server version checks to document identity so custom queries
can reuse packaged operation names without borrowing their compatibility metadata.
Preserve selected operations when exporting playground snippets, update Apollo Client 4
examples, and normalize Bridge MCP templates without overriding GraphQL defaults.

Preserve explicit nulls in Go raw requests, maps, and JSON scalars while omitting
unset typed argument fields.
