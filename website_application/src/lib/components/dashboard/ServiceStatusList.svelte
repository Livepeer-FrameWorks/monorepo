<script lang="ts">
  interface ConnectionStatus {
    status: string;
    message: string;
  }

  type ServiceState = "connected" | "loading" | "error";

  interface Props {
    wsStatus: ConnectionStatus;
    controlPlane: ServiceState;
    dataPlane: ServiceState;
  }

  let { wsStatus, controlPlane, dataPlane }: Props = $props();

  let wsStatusColor = $derived(
    wsStatus.status === "connected"
      ? "bg-success"
      : wsStatus.status === "reconnecting"
        ? "bg-warning animate-pulse"
        : "bg-destructive"
  );

  let controlPlaneColor = $derived(
    controlPlane === "connected"
      ? "bg-success"
      : controlPlane === "loading"
        ? "bg-warning animate-pulse"
        : "bg-destructive"
  );

  let dataPlaneColor = $derived(
    dataPlane === "connected"
      ? "bg-success"
      : dataPlane === "loading"
        ? "bg-warning animate-pulse"
        : "bg-destructive"
  );

  function getStatusLabel(state: ServiceState): string {
    switch (state) {
      case "connected":
        return "Connected";
      case "loading":
        return "Connecting...";
      case "error":
        return "Connection failed";
    }
  }
</script>

<div class="space-y-3">
  <h3 class="font-semibold text-foreground text-sm">Platform Status</h3>

  <div class="space-y-2">
    <!-- Control Plane (Auth) -->
    <div class="flex items-center justify-between p-3 bg-muted">
      <div class="flex items-center space-x-3">
        <div class="w-3 h-3 rounded-full {controlPlaneColor}"></div>
        <span class="text-sm text-foreground">Control Plane</span>
      </div>
      <span class="text-xs text-muted-foreground">{getStatusLabel(controlPlane)}</span>
    </div>

    <!-- Data Plane (GraphQL) -->
    <div class="flex items-center justify-between p-3 bg-muted">
      <div class="flex items-center space-x-3">
        <div class="w-3 h-3 rounded-full {dataPlaneColor}"></div>
        <span class="text-sm text-foreground">Data Plane</span>
      </div>
      <span class="text-xs text-muted-foreground">{getStatusLabel(dataPlane)}</span>
    </div>

    <!-- Real-time (WebSocket) -->
    <div class="flex items-center justify-between p-3 bg-muted">
      <div class="flex items-center space-x-3">
        <div class="w-3 h-3 rounded-full {wsStatusColor}"></div>
        <span class="text-sm text-foreground">Real-time</span>
      </div>
      <span class="text-xs text-muted-foreground capitalize">{wsStatus.message}</span>
    </div>
  </div>
</div>
