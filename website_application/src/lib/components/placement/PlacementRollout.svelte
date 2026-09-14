<script lang="ts">
  import { rolloutLabel, type Rollout } from "$lib/placement/model";
  let {
    rollout,
    revision,
    activeRevision,
    parentRevision,
    activeParentRevision,
  }: {
    rollout: Rollout;
    revision: string;
    activeRevision?: string | null;
    parentRevision?: string;
    activeParentRevision?: string | null;
  } = $props();
</script>

<section class="space-y-2 text-sm" aria-label="Placement enforcement status">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <p class="font-medium">{rolloutLabel(rollout.status)}</p>
    {#if rollout.updatedAt}<p class="text-xs text-muted-foreground">
        Updated {rollout.updatedAt}
      </p>{/if}
  </div>
  {#if rollout.status === "NOT_CONFIGURED" && parentRevision && parentRevision !== "0"}
    <p class="text-muted-foreground">
      This stream follows your account policy.
      {#if activeParentRevision !== parentRevision}
        The newest account change is still rolling out.
      {/if}
    </p>
  {/if}
  <details>
    <summary class="cursor-pointer text-xs text-muted-foreground">Deployment details</summary>
    <div class="mt-2 space-y-2 border-l-2 border-border pl-3">
      <p class="text-muted-foreground">
        Requested revision {revision}; active revision {activeRevision ??
          "not confirmed"}{parentRevision
          ? ` · account revision ${parentRevision}; active account revision ${activeParentRevision ?? "not confirmed"}`
          : ""}.
      </p>
      {#if rollout.existingSessionsRetained}<p class="text-muted-foreground">
          Existing sessions stay connected; changes apply to new routing decisions.
        </p>{/if}
      {#if rollout.requiredRecipients > 0}
        <p>
          {rollout.appliedRecipients} of {rollout.requiredRecipients} required recipients have applied
          this revision.
        </p>
      {:else if rollout.status === "PENDING" || rollout.status === "BLOCKED"}
        <p>Enforcement coverage has not been confirmed.</p>
      {/if}
      {#if rollout.pendingRecipients.length}
        <p>Waiting for:</p>
        <ul class="mt-2 space-y-1">
          {#each rollout.pendingRecipients as recipient (recipient.id)}
            <li>
              {recipient.name}: {recipient.reason ??
                rolloutLabel(recipient.status)}{recipient.authorityExpiresAt
                ? ` · authority expires ${recipient.authorityExpiresAt}`
                : ""}
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </details>
</section>
