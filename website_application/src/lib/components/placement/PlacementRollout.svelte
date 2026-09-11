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
  <p class="font-medium">{rolloutLabel(rollout.status)}</p>
  <p class="text-muted-foreground">
    Requested revision {revision}; active revision {activeRevision ??
      "not confirmed"}{parentRevision
      ? ` · account revision ${parentRevision}; active account revision ${activeParentRevision ?? "not confirmed"}`
      : ""}.
  </p>
  {#if rollout.status === "NOT_CONFIGURED" && parentRevision && parentRevision !== "0"}
    <p>
      This stream inherits the requested account policy; it has no stream override.
      {#if activeParentRevision !== parentRevision}
        Enforcement of that account revision on this stream has not been confirmed.
      {/if}
    </p>
  {/if}
  {#if rollout.requiredRecipients > 0}
    <p>
      {rollout.appliedRecipients} of {rollout.requiredRecipients} required recipients have applied this
      revision.
    </p>
  {:else if rollout.status === "PENDING" || rollout.status === "BLOCKED"}
    <p>Enforcement coverage has not been confirmed.</p>
  {/if}
  {#if rollout.existingSessionsRetained}<p class="text-muted-foreground">
      Already admitted publishers and sessions retain their existing admission. These rules govern
      new decisions.
    </p>{/if}
  {#if rollout.pendingRecipients.length}
    <details>
      <summary class="cursor-pointer"
        >Pending recipients ({rollout.pendingRecipients.length})</summary
      >
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
    </details>
  {/if}
  {#if rollout.updatedAt}<p class="text-xs text-muted-foreground">
      Last updated {rollout.updatedAt}
    </p>{/if}
</section>
