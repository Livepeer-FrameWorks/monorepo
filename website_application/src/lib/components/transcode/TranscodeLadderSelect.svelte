<script lang="ts">
  import { onMount } from "svelte";
  import { TranscodeLadderPolicyStore } from "$houdini";
  import { Label } from "$lib/components/ui/label";
  import { ladderSummary } from "./ladders";

  // A stream's transcode ladder. "" inherits the tenant's ladder, then the
  // plan default; only ladders the plan allows are offered.
  let {
    id,
    value = $bindable(""),
    disabled = false,
    usage = "stream",
  }: {
    id: string;
    value: string;
    disabled?: boolean;
    usage?: "stream" | "upload";
  } = $props();

  const policyStore = new TranscodeLadderPolicyStore();
  let policy = $derived($policyStore.data?.transcodeLadderPolicy ?? null);
  let inherited = $derived(
    policy?.ladders.find((ladder) => ladder.name === policy?.effectiveLadder) ?? null
  );
  let selected = $derived(policy?.ladders.find((ladder) => ladder.name === value) ?? inherited);
  // A stream may hold a ladder its plan no longer allows; it then gets the
  // inherited one, which the hint says.
  let unavailable = $derived(
    value !== "" && policy !== null && !policy.ladders.some((ladder) => ladder.name === value)
  );

  let loadFailed = $state(false);

  onMount(() => {
    policyStore.fetch().catch(() => {
      loadFailed = true;
    });
  });
</script>

<div class="space-y-2">
  <Label for={id} class="text-sm text-foreground">Transcode ladder</Label>
  <select
    {id}
    bind:value
    disabled={disabled || policy === null}
    class="w-full rounded-none border border-input bg-background px-3 py-2 text-sm"
  >
    <option value=""
      >Inherit ({inherited?.displayName ?? policy?.effectiveLadder ?? "plan default"})</option
    >
    {#each policy?.ladders ?? [] as ladder (ladder.name)}
      <option value={ladder.name}>{ladder.displayName}</option>
    {/each}
    {#if unavailable}
      <option {value}>{value} (not on your plan)</option>
    {/if}
  </select>
  {#if loadFailed}
    <p class="text-xs text-destructive">
      Transcode ladders could not be loaded; the current ladder is kept.
    </p>
  {:else if unavailable}
    <p class="text-xs text-amber-600 dark:text-amber-400">
      Your plan no longer includes this ladder; the inherited ladder applies.
    </p>
  {:else if selected}
    <p class="text-xs text-muted-foreground">
      {ladderSummary(selected.renditionNames)}. Renditions above the source size are skipped. Takes
      effect {usage === "upload" ? "for this upload" : "from the next broadcast"}.
    </p>
  {/if}
</div>
