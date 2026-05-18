<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { MediaRetentionPolicyStore, SetMediaRetentionPolicyStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast";
  import { HardDrive } from "lucide-svelte";

  // Per-asset-class retention defaults. The tier cap (bound) is the upper
  // bound on what a tenant can write; 0 = no cap (paid baseline). A
  // user-typed value of 0 means "keep forever" — only honored on uncapped
  // tiers; Free's cap clamps it at write time on the server.

  const policyStore = new MediaRetentionPolicyStore();
  const setPolicyMutation = new SetMediaRetentionPolicyStore();

  type ClassKey = "VOD" | "DVR" | "CLIP";

  let saving = $state(false);
  let savingTarget = $state<ClassKey | null>(null);

  let policy = $derived($policyStore.data?.mediaRetentionPolicy ?? null);
  let bound = $derived(policy?.bounds.maxRecordingRetentionDays ?? 0);
  let uncapped = $derived(bound === 0);

  // Form state mirrors the persisted per-class columns (null = no override).
  // The empty-string sentinel keeps the input controlled when the user
  // clears the field; a literal user-typed 0 maps to days=0 (keep forever).
  let vodInput = $state<number | "" | null>(null);
  let dvrInput = $state<number | "" | null>(null);
  let clipInput = $state<number | "" | null>(null);

  $effect(() => {
    if (policy) {
      vodInput = policy.defaultVodRetentionDays ?? null;
      dvrInput = policy.defaultDvrRetentionDays ?? null;
      clipInput = policy.defaultClipRetentionDays ?? null;
    }
  });

  onMount(async () => {
    await policyStore.fetch();
  });

  function parseInput(raw: string): number | "" | null {
    if (raw === "") return "";
    const n = Number(raw);
    if (!Number.isFinite(n)) return null;
    return n;
  }

  function asDays(value: number | "" | null): number | null {
    if (value === null || value === "") return null;
    return value;
  }

  function validateValue(value: number | null): string | null {
    if (value === null) return "Value is required";
    if (value < 0) return "Days must be 0 or greater";
    if (!uncapped && value > bound) return `Days must be at most your tier cap (${bound})`;
    if (!uncapped && value === 0)
      return "Your tier requires a finite retention (0 = keep forever is paid-tier only)";
    return null;
  }

  function describePerClass(
    value: number | null | undefined,
    effective: number,
    fallback: string
  ): string {
    if (value === null || value === undefined) {
      const eff = effective === 0 ? "keep forever" : `${effective} days`;
      return `(inheriting — currently ${eff} per ${fallback})`;
    }
    if (value === 0) return "Keep forever";
    return `${value} days`;
  }

  async function saveClass(target: ClassKey, value: number | "" | null) {
    const days = asDays(value);
    const err = validateValue(days);
    if (err) {
      toast.error(err);
      return;
    }
    saving = true;
    savingTarget = target;
    try {
      const result = await setPolicyMutation.mutate({
        input: { targetType: target, days: days! },
      });
      const data = result.data?.setMediaRetentionPolicy;
      if (data && "effectiveVodRetentionDays" in data) {
        toast.success(`${target} retention default updated`);
        await policyStore.fetch({ policy: "NetworkOnly" });
      } else if (data && "message" in data) {
        toast.error(String(data.message));
      }
    } catch (err) {
      toast.error(`Save failed: ${(err as Error).message}`);
    } finally {
      saving = false;
      savingTarget = null;
    }
  }

  async function clearClass(target: ClassKey) {
    saving = true;
    savingTarget = target;
    try {
      const result = await setPolicyMutation.mutate({
        input: { targetType: target, clear: true },
      });
      const data = result.data?.setMediaRetentionPolicy;
      if (data && "effectiveVodRetentionDays" in data) {
        toast.success(`${target} retention cleared — inheriting`);
        await policyStore.fetch({ policy: "NetworkOnly" });
      } else if (data && "message" in data) {
        toast.error(String(data.message));
      }
    } catch (err) {
      toast.error(`Clear failed: ${(err as Error).message}`);
    } finally {
      saving = false;
      savingTarget = null;
    }
  }
</script>

<div class="slab">
  <div class="slab-header">
    <div class="flex items-center gap-2">
      <HardDrive class="w-4 h-4 text-primary" />
      <h3>Storage retention defaults</h3>
    </div>
  </div>
  <div class="slab-body--padded">
    <p class="text-sm text-muted-foreground mb-4">
      How long new artifacts stay around before automatic deletion. VOD uploads default to keep
      forever; DVR and clips default to 30 days. Per-asset overrides happen in
      <a href={resolve("/library")} class="text-primary hover:underline">your library</a>.
    </p>

    {#if $policyStore.fetching && !policy}
      <p class="text-sm text-muted-foreground">Loading policy…</p>
    {:else if policy}
      <div class="space-y-4 max-w-2xl">
        <div class="text-xs text-muted-foreground">
          {#if uncapped}
            Your tier has <strong>no cap</strong> — set any value including <strong>0</strong> for keep-forever.
          {:else}
            Your tier caps retention at <strong>{bound} days</strong> per class.
          {/if}
        </div>

        {#each [{ key: "VOD" as ClassKey, label: "VOD uploads", persisted: policy.defaultVodRetentionDays, effective: policy.effectiveVodRetentionDays, value: vodInput, set: (v: number | "" | null) => (vodInput = v) }, { key: "DVR" as ClassKey, label: "DVR recordings", persisted: policy.defaultDvrRetentionDays, effective: policy.effectiveDvrRetentionDays, value: dvrInput, set: (v: number | "" | null) => (dvrInput = v) }, { key: "CLIP" as ClassKey, label: "Clips", persisted: policy.defaultClipRetentionDays, effective: policy.effectiveClipRetentionDays, value: clipInput, set: (v: number | "" | null) => (clipInput = v) }] as row (row.key)}
          <div class="grid grid-cols-12 gap-3 items-end border-t pt-3">
            <div class="col-span-4">
              <Label class="text-xs">{row.label}</Label>
              <div class="text-xs text-muted-foreground">
                Currently: {describePerClass(row.persisted, row.effective, "tier")}
              </div>
            </div>
            <div class="col-span-3">
              <Input
                type="number"
                min="0"
                max={uncapped ? undefined : bound}
                value={row.value ?? ""}
                oninput={(e) => row.set(parseInput((e.target as HTMLInputElement).value))}
                disabled={saving}
                placeholder="days"
              />
            </div>
            <div class="col-span-5 flex gap-2">
              <Button
                onclick={() => saveClass(row.key, row.value)}
                disabled={saving || row.value === null || row.value === ""}
              >
                {saving && savingTarget === row.key ? "Saving…" : "Save"}
              </Button>
              <Button variant="outline" onclick={() => clearClass(row.key)} disabled={saving}>
                Inherit
              </Button>
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <p class="text-sm text-destructive">Failed to load retention policy.</p>
    {/if}
  </div>
</div>
