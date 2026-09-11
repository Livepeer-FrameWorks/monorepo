# Placement controls fixture

Run `pnpm --dir website_application dev:placement-fixture`, then open
`http://127.0.0.1:5179/`. The server binds only to loopback.

This uses the real shared rules and rollout components with application styles and labelled
in-memory fixtures. It does not connect to a gateway, authenticate a tenant, mutate policies,
subscribe to capacity, prepare media or reserve nodes. It is separate from gateway demo mode
and database seed data.

Use it to check desktop/360px wrapping, long selector names, expanded restrictions, fallback
fields, keyboard reordering, preset replacement, disabled controls and minimum 44px targets.
Run `pnpm --dir website_application test:components` for complete editor interactions with
mocked API boundaries, and `pnpm --dir website_application test` for controller/contract tests.
Neither fixtures nor component tests substitute for backend integration and first-media tests.

The owner consent editor uses its real controller with an in-memory consent API and a labelled
fixture identity. Review/acknowledge/apply changes only this process-local fixture. Navigation
guards are exercised in component tests; standalone fixture navigation is not SvelteKit routing.
