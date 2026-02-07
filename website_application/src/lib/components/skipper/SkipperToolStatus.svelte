<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    toolName: string;
    error?: string;
  }

  let { toolName, error = "" }: Props = $props();

  const toolLabels: Record<string, { label: string; icon: string }> = {
    search_knowledge: { label: "Searching knowledge base", icon: "BookOpen" },
    search_web: { label: "Searching the web", icon: "Globe" },
    diagnose_rebuffering: { label: "Diagnosing rebuffering", icon: "Activity" },
    diagnose_buffer_health: { label: "Diagnosing buffer health", icon: "Activity" },
    diagnose_packet_loss: { label: "Diagnosing packet loss", icon: "Activity" },
    diagnose_routing: { label: "Analyzing viewer routing", icon: "Network" },
    get_stream_health_summary: { label: "Checking stream health", icon: "HeartPulse" },
    get_anomaly_report: { label: "Detecting anomalies", icon: "AlertTriangle" },
    create_stream: { label: "Creating stream", icon: "Radio" },
    create_clip: { label: "Creating clip", icon: "Scissors" },
    create_vod_upload: { label: "Uploading VOD", icon: "Upload" },
    check_topup: { label: "Checking billing", icon: "CreditCard" },
    topup_balance: { label: "Processing top-up", icon: "CreditCard" },
    introspect_schema: { label: "Reading API schema", icon: "FileCode" },
    generate_query: { label: "Generating query", icon: "FileCode" },
    execute_query: { label: "Querying API", icon: "Database" },
    search_support_history: { label: "Searching support history", icon: "Search" },
    update_stream: { label: "Updating stream", icon: "Radio" },
    delete_stream: { label: "Deleting stream", icon: "Radio" },
    refresh_stream_key: { label: "Refreshing stream key", icon: "Radio" },
    delete_clip: { label: "Deleting clip", icon: "Scissors" },
    start_dvr: { label: "Starting DVR recording", icon: "Circle" },
    stop_dvr: { label: "Stopping DVR recording", icon: "Square" },
    complete_vod_upload: { label: "Completing upload", icon: "Upload" },
    abort_vod_upload: { label: "Aborting upload", icon: "Upload" },
    delete_vod_asset: { label: "Deleting VOD asset", icon: "Trash2" },
    resolve_playback_endpoint: { label: "Resolving playback", icon: "Play" },
    update_billing_details: { label: "Updating billing", icon: "CreditCard" },
    get_payment_options: { label: "Loading payment options", icon: "CreditCard" },
    submit_payment: { label: "Processing payment", icon: "CreditCard" },
  };

  let info = $derived(toolLabels[toolName] ?? { label: toolName, icon: "Wrench" });
  let IconComponent = $derived(getIconComponent(info.icon));
</script>

<div class="flex items-center gap-2 rounded-lg border border-border/50 bg-muted/30 px-3 py-2">
  {#if error}
    <span class="h-2 w-2 shrink-0 rounded-full bg-destructive"></span>
  {:else}
    <span class="h-2 w-2 shrink-0 animate-pulse rounded-full bg-primary"></span>
  {/if}
  <IconComponent class="h-3.5 w-3.5 text-muted-foreground" />
  <span class="text-xs text-muted-foreground">
    {#if error}
      {info.label} failed
    {:else}
      {info.label}...
    {/if}
  </span>
</div>
