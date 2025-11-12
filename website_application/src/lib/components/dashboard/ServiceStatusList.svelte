<script lang="ts">
  interface ConnectionStatus {
    status: string;
    message: string;
  }

  interface Props {
    wsStatus: ConnectionStatus;
    analyticsLoaded: boolean;
  }

  let { wsStatus, analyticsLoaded }: Props = $props();

  let wsStatusColor = $derived(
    wsStatus.status === "connected"
      ? "bg-tokyo-night-green"
      : wsStatus.status === "reconnecting"
      ? "bg-tokyo-night-yellow animate-pulse"
      : "bg-tokyo-night-red"
  );

  let analyticsStatusColor = $derived(
    analyticsLoaded ? "bg-tokyo-night-green" : "bg-tokyo-night-yellow"
  );
</script>

<div class="space-y-3">
  <h3 class="font-semibold text-tokyo-night-fg text-sm">Platform Status</h3>

  <div class="space-y-2">
    <!-- WebSocket Connection -->
    <div
      class="flex items-center justify-between p-3 bg-tokyo-night-bg-highlight rounded-lg"
    >
      <div class="flex items-center space-x-3">
        <div class="w-3 h-3 rounded-full {wsStatusColor}"></div>
        <span class="text-sm text-tokyo-night-fg">Real-time Updates</span>
      </div>
      <span class="text-xs text-tokyo-night-comment capitalize"
        >{wsStatus.message}</span
      >
    </div>

    <!-- Streaming Service -->
    <div
      class="flex items-center justify-between p-3 bg-tokyo-night-bg-highlight rounded-lg"
    >
      <div class="flex items-center space-x-3">
        <div class="w-3 h-3 rounded-full bg-tokyo-night-green"></div>
        <span class="text-sm text-tokyo-night-fg">Streaming Service</span>
      </div>
      <span class="text-xs text-tokyo-night-comment">Operational</span>
    </div>

    <!-- Analytics Service -->
    <div
      class="flex items-center justify-between p-3 bg-tokyo-night-bg-highlight rounded-lg"
    >
      <div class="flex items-center space-x-3">
        <div class="w-3 h-3 rounded-full {analyticsStatusColor}"></div>
        <span class="text-sm text-tokyo-night-fg">Analytics Service</span>
      </div>
      <span class="text-xs text-tokyo-night-comment"
        >{analyticsLoaded ? "Operational" : "Loading"}</span
      >
    </div>
  </div>
</div>
