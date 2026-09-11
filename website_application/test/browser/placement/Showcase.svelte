<script lang="ts">
  import MediaCapacityConsentEditor from "$lib/components/placement/MediaCapacityConsentEditor.svelte";
  import PlacementRulesEditor from "$lib/components/placement/PlacementRulesEditor.svelte";
  import PlacementRollout from "$lib/components/placement/PlacementRollout.svelte";
  import { presetRules, type Policy, type Rules } from "$lib/placement/model";
  let rules = $state<Rules | null>(presetRules("my_clusters_first"));
  const features: Policy["features"] = {
    schemaVersion: 1,
    geographicSpillover: true,
    priceOrdering: false,
    supportedPresets: ["closest_available", "my_clusters_first", "my_clusters_only", "no_official"],
  };
  const rollout: Policy["rollout"] = {
    status: "PENDING",
    requiredRecipients: 2,
    appliedRecipients: 1,
    pendingRecipients: [
      {
        id: "fixture-us",
        name: "US control cell (fixture)",
        status: "PENDING",
        reason: "Waiting for signed authority acknowledgement",
        authorityExpiresAt: null,
      },
    ],
    existingSessionsRetained: true,
    updatedAt: null,
  };
</script>

<main class="max-w-6xl mx-auto p-3 sm:p-6">
  <p class="mb-4 border border-warning p-3 text-sm">
    LOCAL UI FIXTURE · Simulated policy and status. No live API, subscriptions, media work or
    database writes.
  </p>
  <MediaCapacityConsentEditor clusterId="fixture-eu" />
  <div class="slab">
    <div class="slab-header"><h1>Account / Media placement</h1></div>
    <div class="slab-body--padded">
      <PlacementRollout {rollout} revision="12" activeRevision="11" />
    </div>
    <div class="grid grid-cols-1 lg:grid-cols-2 border-t border-border">
      <div class="p-4 sm:p-6 min-w-0 lg:border-r border-border">
        <PlacementRulesEditor
          {rules}
          scope={{ kind: "TENANT" }}
          {features}
          onchange={(value) => (rules = value)}
        />
      </div>
      <section class="p-4 sm:p-6 min-w-0 border-t lg:border-t-0 border-border space-y-3">
        <h2>Preview explanation · fixture</h2>
        <p class="text-sm text-muted-foreground">
          The full preview controller is covered by component tests. This fixture only exercises the
          shared controls and their real styles.
        </p>
        <p class="text-sm">
          Unknown preferred capacity is not verified exhaustion. A US viewer’s distance is measured
          to the destination, not to the EU ingest coordinator.
        </p>
      </section>
    </div>
  </div>
</main>
