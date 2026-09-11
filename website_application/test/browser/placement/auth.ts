import { readable } from "svelte/store";
export const auth = readable({
  isAuthenticated: true,
  user: { id: "fixture-owner", tenant_id: "fixture-tenant", role: "owner" },
});
