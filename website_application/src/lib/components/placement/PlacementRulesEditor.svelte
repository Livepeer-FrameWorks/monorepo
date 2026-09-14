<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PlacementSelectorEditor from "./PlacementSelectorEditor.svelte";
  import {
    copyRules,
    matchingPreset,
    newGroup,
    presetDescriptions,
    presetLabels,
    presetRules,
    selectorLabel,
    type Group,
    type Policy,
    type Rules,
    type Scope,
    type Selector,
  } from "$lib/placement/model";

  let {
    rules,
    scope,
    features,
    disabled = false,
    onchange,
  }: {
    rules: Rules | null;
    scope: Scope;
    features: Policy["features"];
    disabled?: boolean;
    onchange: (value: Rules | null) => void;
  } = $props();
  let preset = $state("");
  let replacePending = $state(false);
  let announcement = $state("");
  const groups = $derived(rules?.preferences?.groups ?? []);
  const supported = $derived(features.supportedPresets.filter((id) => !!presetLabels[id]));
  const activePreset = $derived(matchingPreset(rules, supported));

  function change(update: (draft: Rules) => void) {
    const draft = copyRules(rules) ?? {
      schemaVersion: 1,
      constraints: { deny: [] },
      preferences: null,
    };
    update(draft);
    onchange(draft);
  }

  function applyPreset() {
    if (!supported.includes(preset)) return;
    onchange(presetRules(preset));
    replacePending = false;
  }

  function choosePreset(next: string) {
    if (activePreset === next) return;
    preset = next;
    if (rules && !activePreset) {
      replacePending = true;
      return;
    }
    applyPreset();
  }

  function editGroup(index: number, update: Partial<Group>) {
    change((draft) => {
      if (draft.preferences?.groups[index])
        draft.preferences.groups[index] = { ...draft.preferences.groups[index], ...update };
    });
  }

  function move(index: number, delta: number) {
    const target = index + delta;
    if (target < 0 || target >= groups.length) return;
    change((draft) => {
      const items = draft.preferences!.groups;
      [items[index], items[target]] = [items[target], items[index]];
      announcement = `Group moved to position ${target + 1} of ${items.length}. Its fallback condition moved with it.`;
    });
  }

  function selectorChange(kind: "allow" | "deny", index: number, value: Selector) {
    change((draft) => {
      if (kind === "allow" && draft.constraints.allow) draft.constraints.allow.any[index] = value;
      else if (kind === "deny") draft.constraints.deny[index] = value;
    });
  }

  function removeSelector(kind: "allow" | "deny", index: number) {
    change((draft) => {
      if (kind === "allow" && draft.constraints.allow) draft.constraints.allow.any.splice(index, 1);
      else if (kind === "deny") draft.constraints.deny.splice(index, 1);
    });
  }
</script>

<fieldset
  {disabled}
  class="space-y-5 min-w-0 [&_button]:min-h-11 [&_button]:min-w-11 [&_button]:whitespace-normal [&_select]:min-h-11 [&_input:not([type=checkbox])]:min-h-11 [&_summary]:min-h-11 [&_summary]:py-2 [&_label]:min-h-11"
>
  <legend class="sr-only">Placement rules</legend>
  <section class="space-y-3" aria-label="Placement approach">
    <div>
      <h3 class="font-semibold">Choose an approach</h3>
      <p class="text-sm text-muted-foreground">
        Start with the outcome you want. You can fine-tune it under Advanced rules.
      </p>
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 border-t border-l border-border">
      <Button
        class="h-auto min-h-20 items-start justify-start rounded-none border-r border-b border-border px-4 py-3 text-left"
        variant={!rules ? "secondary" : "ghost"}
        aria-pressed={!rules}
        onclick={() => {
          replacePending = false;
          onchange(null);
        }}
      >
        <span>
          <span class="block font-medium">
            {scope.kind === "STREAM" ? "Follow account policy" : "Platform default"}
          </span>
          <span class="mt-1 block text-xs font-normal text-muted-foreground">
            {scope.kind === "STREAM"
              ? "Keep this stream aligned with your account-level placement policy."
              : "Use the platform defaults and closest eligible capacity."}
          </span>
        </span>
      </Button>
      {#each supported as id (id)}
        <Button
          class="h-auto min-h-20 items-start justify-start rounded-none border-r border-b border-border px-4 py-3 text-left"
          variant={activePreset === id ? "secondary" : "ghost"}
          aria-pressed={activePreset === id}
          onclick={() => choosePreset(id)}
        >
          <span>
            <span class="block font-medium">{presetLabels[id]}</span>
            <span class="mt-1 block text-xs font-normal text-muted-foreground">
              {presetDescriptions[id]}
            </span>
          </span>
        </Button>
      {/each}
    </div>
    {#if replacePending}
      <div class="border-l-2 border-warning pl-3 text-sm space-y-2">
        <p>
          This replaces the advanced draft for {presetLabels[preset]}. Account restrictions remain
          enforced.
        </p>
        <Button size="sm" onclick={applyPreset}>Replace advanced draft</Button>
        <Button size="sm" variant="ghost" onclick={() => (replacePending = false)}>Cancel</Button>
      </div>
    {/if}
  </section>

  <details class="border border-border">
    <summary class="cursor-pointer px-4 py-3 text-sm font-medium">
      Advanced rules{rules && !activePreset ? " · Custom" : ""}
    </summary>
    <div class="space-y-5 border-t border-border p-4">
      {#if !rules}
        <p class="text-sm text-muted-foreground">
          This scope currently follows {scope.kind === "STREAM"
            ? "the account policy"
            : "system defaults"}. Start a custom policy only when the approaches above are not
          specific enough.
        </p>
        <Button variant="outline" onclick={() => change(() => {})}>Start a custom policy</Button>
      {:else}
        {#if !features.priceOrdering}<p class="text-xs text-muted-foreground">
            Cost-first ordering is unavailable until this deployment supplies comparable effective
            prices.
          </p>{/if}
        <section class="space-y-3" aria-label="Hard restrictions">
          <h3 class="text-sm font-semibold">Hard restrictions</h3>
          <p class="text-xs text-muted-foreground">
            Hard restrictions apply to every group. Empty allowed alternatives deliberately deny all
            destinations; this is not inheritance.
          </p>
          <label class="text-sm block"
            >Allowed capacity
            <select
              class="block w-full mt-1 p-2 bg-background border border-border"
              value={rules.constraints.allow ? "restricted" : "inherit"}
              onchange={(event) =>
                change(
                  (draft) =>
                    (draft.constraints.allow =
                      event.currentTarget.value === "inherit" ? null : { any: [] })
                )}
            >
              <option value="inherit">No additional restriction</option>
              <option value="restricted">Only matching alternatives below</option>
            </select>
          </label>
          {#if rules.constraints.allow}
            {#if rules.constraints.allow.any.length === 0}<p class="text-sm text-warning">
                No allowed alternatives: new destinations will be denied.
              </p>{/if}
            {#each rules.constraints.allow.any as selector, index (index)}
              <details class="border border-border p-3">
                <summary class="cursor-pointer text-sm"
                  >Allow alternative {index + 1}: {selectorLabel(selector)}</summary
                >
                <div class="pt-3 space-y-3">
                  <PlacementSelectorEditor
                    value={selector}
                    {scope}
                    {disabled}
                    onchange={(value) => selectorChange("allow", index, value)}
                  />
                  <Button size="sm" variant="ghost" onclick={() => removeSelector("allow", index)}
                    >Remove allowed alternative</Button
                  >
                </div>
              </details>
            {/each}
            <Button
              size="sm"
              variant="outline"
              onclick={() => change((draft) => draft.constraints.allow?.any.push({}))}
              >Add allowed alternative</Button
            >
          {/if}
          <h4 class="text-sm font-medium">Never use</h4>
          {#each rules.constraints.deny as selector, index (index)}
            <details class="border border-border p-3">
              <summary class="cursor-pointer text-sm"
                >Deny {index + 1}: {selectorLabel(selector)}</summary
              >
              <div class="pt-3 space-y-3">
                <PlacementSelectorEditor
                  value={selector}
                  {scope}
                  {disabled}
                  onchange={(value) => selectorChange("deny", index, value)}
                />
                <Button size="sm" variant="ghost" onclick={() => removeSelector("deny", index)}
                  >Remove deny rule</Button
                >
              </div>
            </details>
          {/each}
          <div class="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="outline"
              onclick={() =>
                change((draft) => draft.constraints.deny.push({ classes: ["PLATFORM_OFFICIAL"] }))}
              >Exclude official</Button
            >
            <Button
              size="sm"
              variant="outline"
              onclick={() =>
                change((draft) =>
                  draft.constraints.deny.push({
                    classes: ["PLATFORM_OFFICIAL"],
                    charging: ["RATED"],
                  })
                )}>Exclude rated official</Button
            >
            <Button
              size="sm"
              variant="outline"
              onclick={() => change((draft) => draft.constraints.deny.push({}))}
              >Add custom deny</Button
            >
          </div>
          <p class="text-xs text-muted-foreground">
            An empty custom deny matches every destination. Choose its matching fields before
            reviewing.
          </p>
        </section>

        <section class="space-y-3 border-t border-border pt-4" aria-label="Capacity order">
          <h3 class="text-sm font-semibold">Capacity order</h3>
          <label class="text-sm block"
            >Preference order
            <select
              class="block w-full mt-1 p-2 bg-background border border-border"
              value={rules.preferences ? "custom" : "inherit"}
              onchange={(event) =>
                change(
                  (draft) =>
                    (draft.preferences =
                      event.currentTarget.value === "inherit" ? null : { groups: [] })
                )}
            >
              <option value="inherit">Inherit preference order</option>
              <option value="custom">Use groups below</option>
            </select>
          </label>
          {#if rules.preferences}
            {#if groups.length === 0}<p class="text-sm text-warning">
                No groups: new destinations will be denied. Add a group or explicitly inherit.
              </p>{/if}
            <p class="sr-only" aria-live="polite">{announcement}</p>
            {#each groups as group, index (group.id)}
              <div class="border border-border">
                <div class="p-3 space-y-3">
                  <div class="flex items-center justify-between gap-2">
                    <h4 class="text-sm font-medium">{index + 1}. {selectorLabel(group.match)}</h4>
                    <div class="flex gap-1 shrink-0">
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={index === 0}
                        aria-label={`Move group ${index + 1} up`}
                        onclick={() => move(index, -1)}>↑</Button
                      >
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={index === groups.length - 1}
                        aria-label={`Move group ${index + 1} down`}
                        onclick={() => move(index, 1)}>↓</Button
                      >
                    </div>
                  </div>
                  <details>
                    <summary class="cursor-pointer text-sm">Matching rules</summary>
                    <div class="pt-3">
                      <PlacementSelectorEditor
                        value={group.match}
                        {scope}
                        {disabled}
                        onchange={(match) => editGroup(index, { match })}
                      />
                    </div>
                  </details>
                  <label class="text-sm block"
                    >Order within this group
                    <select
                      class="block w-full mt-1 p-2 bg-background border border-border"
                      value={group.order}
                      onchange={(event) =>
                        editGroup(index, {
                          order: event.currentTarget.value === "PRICE" ? "PRICE" : "DISTANCE",
                        })}
                    >
                      <option value="DISTANCE">Closest available</option>
                      <option value="PRICE" disabled={!features.priceOrdering}
                        >Lowest comparable price</option
                      >
                    </select>
                  </label>
                  {#if group.order === "PRICE"}
                    <label class="text-sm block"
                      >Currency<Input
                        value={group.priceCurrency ?? ""}
                        onchange={(event) =>
                          editGroup(index, { priceCurrency: event.currentTarget.value })}
                        placeholder="USD"
                      /></label
                    >
                    <label class="text-sm block"
                      >Comparable unit<Input
                        value={group.priceUnit ?? ""}
                        onchange={(event) =>
                          editGroup(index, { priceUnit: event.currentTarget.value })}
                        placeholder="Unit supplied by pricing"
                      /></label
                    >
                  {/if}
                  <details>
                    <summary class="cursor-pointer text-sm">Distance and fallback</summary>
                    <div class="pt-3 space-y-3">
                      <label class="text-sm block"
                        >Hard maximum distance (km; 0 = unbounded)<Input
                          type="number"
                          min="0"
                          value={group.maxDistanceKm}
                          oninput={(event) =>
                            editGroup(index, {
                              maxDistanceKm:
                                event.currentTarget.value === ""
                                  ? NaN
                                  : Number(event.currentTarget.value),
                            })}
                        /></label
                      >
                      <label class="text-sm block"
                        >Use the next group
                        <select
                          class="block w-full mt-1 p-2 bg-background border border-border"
                          value={group.spillover}
                          onchange={(event) =>
                            editGroup(index, {
                              spillover: event.currentTarget.value as Group["spillover"],
                            })}
                        >
                          <option value="NEVER">Never</option><option value="CAPACITY_ONLY"
                            >When verified capacity is exhausted</option
                          >
                          <option value="GEO_HOLE" disabled={!features.geographicSpillover}
                            >When the geographic limit is exceeded</option
                          >
                          <option
                            value="CAPACITY_OR_GEO_HOLE"
                            disabled={!features.geographicSpillover}
                            >When either condition is met</option
                          >
                        </select>
                      </label>
                      {#if group.spillover === "GEO_HOLE" || group.spillover === "CAPACITY_OR_GEO_HOLE"}
                        <label class="text-sm block"
                          >Geographic limit (km)<Input
                            type="number"
                            min="0"
                            value={group.geoHoleDistanceKm}
                            oninput={(event) =>
                              editGroup(index, {
                                geoHoleDistanceKm:
                                  event.currentTarget.value === ""
                                    ? NaN
                                    : Number(event.currentTarget.value),
                              })}
                          /></label
                        >
                        <label class="text-sm block"
                          >Minimum improvement (km)<Input
                            type="number"
                            min="0"
                            value={group.minImprovementKm}
                            oninput={(event) =>
                              editGroup(index, {
                                minImprovementKm:
                                  event.currentTarget.value === ""
                                    ? NaN
                                    : Number(event.currentTarget.value),
                              })}
                          /></label
                        >
                      {/if}
                      <p class="text-xs text-muted-foreground">
                        Unknown capacity and unreachable cells are not verified exhaustion. Unknown
                        geography does not authorize geographic fallback.
                      </p>
                      {#if index === groups.length - 1 && group.spillover !== "NEVER"}<p
                          class="text-sm text-warning"
                        >
                          There is no next group. This condition cannot supply additional capacity.
                        </p>{/if}
                    </div>
                  </details>
                </div>
                <Button
                  class="w-full rounded-none border-t border-border"
                  variant="ghost"
                  onclick={() => change((draft) => draft.preferences?.groups.splice(index, 1))}
                  >Remove group {index + 1}</Button
                >
              </div>
            {/each}
            <Button
              variant="outline"
              onclick={() =>
                change((draft) => draft.preferences?.groups.push(newGroup(crypto.randomUUID())))}
              >Add capacity group</Button
            >
          {/if}
        </section>
      {/if}
    </div>
  </details>
</fieldset>
