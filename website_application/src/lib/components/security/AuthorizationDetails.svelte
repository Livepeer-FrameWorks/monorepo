<script lang="ts">
  import {
    authorizationClientName,
    authorizationRemainingSeconds,
    type AuthorizationPreview,
  } from "$lib/auth/consent";
  import { securityTimestamp } from "$lib/utils/security-time";
  let { request, now }: { request: AuthorizationPreview; now: number } = $props();
  const remaining = $derived(authorizationRemainingSeconds(request.expires_at, now));
</script>

<dl class="text-sm divide-y divide-border">
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Client</dt>
    <dd>{authorizationClientName(request.client_id)}</dd>
  </div>
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Device</dt>
    <dd class="text-right break-all">{request.device_label || "Device name unavailable"}</dd>
  </div>
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Version / OS</dt>
    <dd class="text-right break-all">{request.client_version || "Version unavailable"}</dd>
  </div>
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Request address</dt>
    <dd class="text-right">
      <span class="font-mono">{request.request_ip || "Address unavailable"}</span
      >{#if request.location}<span class="block text-xs text-muted-foreground"
          >{request.location}</span
        >{/if}
    </dd>
  </div>
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Requested</dt>
    <dd class="text-right">{securityTimestamp(request.requested_at)}</dd>
  </div>
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Expires</dt>
    <dd class="text-right">
      {remaining === null
        ? "Expiry unavailable"
        : remaining === 0
          ? "Request expired"
          : `${Math.floor(remaining / 60)}m ${remaining % 60}s remaining`}<span
        class="block text-xs text-muted-foreground"
        >{request.expires_at ? securityTimestamp(request.expires_at) : ""}</span
      >
    </dd>
  </div>
  <div class="py-2 flex justify-between gap-4">
    <dt class="text-muted-foreground">Scope</dt>
    <dd>{request.scope || "account"}</dd>
  </div>
</dl>
<p class="mt-4 text-sm text-warning">Only approve if you started this sign-in yourself.</p>
<p class="mt-2 text-xs text-muted-foreground">
  Device names are supplied by the requesting app. Check them against the device you are signing in
  from.
</p>
