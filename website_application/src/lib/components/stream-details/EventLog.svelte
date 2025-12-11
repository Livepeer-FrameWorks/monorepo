<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";

  export type EventType =
    | "stream_start"
    | "stream_end"
    | "viewer_connect"
    | "viewer_disconnect"
    | "quality_change"
    | "error"
    | "warning"
    | "dvr_start"
    | "dvr_stop"
    | "track_change"
    | "node_health"
    | "service_status"
    | "info";

  export interface StreamEvent {
    id: string;
    timestamp: string;
    type: EventType;
    message: string;
    details?: string;
    streamName?: string;
    nodeName?: string;
  }

  interface Props {
    events: StreamEvent[];
    title?: string;
    maxVisible?: number;
    collapsed?: boolean;
    onToggle?: () => void;
    showStreamName?: boolean;
    emptyMessage?: string;
  }

  let {
    events,
    title = "Event Log",
    maxVisible = 10,
    collapsed = false,
    onToggle,
    showStreamName = false,
    emptyMessage = "No events recorded",
  }: Props = $props();

  let visibleCount = $state(maxVisible);

  function getEventIcon(type: EventType): string {
    switch (type) {
      case "stream_start": return "Play";
      case "stream_end": return "Square";
      case "viewer_connect": return "UserPlus";
      case "viewer_disconnect": return "UserMinus";
      case "quality_change": return "Sliders";
      case "error": return "XCircle";
      case "warning": return "AlertTriangle";
      case "dvr_start": return "Circle";
      case "dvr_stop": return "CircleStop";
      case "track_change": return "Layers";
      case "node_health": return "Server";
      case "service_status": return "Activity";
      case "info":
      default: return "Info";
    }
  }

  function getEventColor(type: EventType): string {
    switch (type) {
      case "stream_start":
      case "viewer_connect":
      case "dvr_start":
        return "text-success";
      case "stream_end":
      case "viewer_disconnect":
      case "dvr_stop":
        return "text-muted-foreground";
      case "error":
        return "text-error";
      case "warning":
        return "text-warning";
      case "quality_change":
      case "track_change":
        return "text-info";
      case "node_health":
      case "service_status":
        return "text-accent-purple";
      default:
        return "text-foreground";
    }
  }

  function formatTime(timestamp: string): string {
    const date = new Date(timestamp);
    return date.toLocaleTimeString("en-US", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    });
  }

  function formatDate(timestamp: string): string {
    const date = new Date(timestamp);
    const now = new Date();
    const isToday = date.toDateString() === now.toDateString();

    if (isToday) {
      return "Today";
    }

    const yesterday = new Date(now);
    yesterday.setDate(yesterday.getDate() - 1);
    if (date.toDateString() === yesterday.toDateString()) {
      return "Yesterday";
    }

    return date.toLocaleDateString("en-US", { month: "short", day: "numeric" });
  }

  function loadMore() {
    visibleCount = Math.min(visibleCount + 10, events.length);
  }

  let visibleEvents = $derived(events.slice(0, visibleCount));
  let hasMore = $derived(events.length > visibleCount);

  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
</script>

<div class="border border-border">
  <!-- Header -->
<button
  type="button"
  onclick={onToggle}
  class="w-full flex items-center justify-between px-4 py-3 bg-brand-surface-muted hover:bg-muted/50 transition-colors cursor-pointer"
  aria-expanded={!collapsed}
  aria-controls="event-log-panel"
  disabled={!onToggle}
>
    <div class="flex items-center gap-2">
      <span class="font-medium text-sm text-foreground">{title}</span>
      {#if events.length > 0}
        <span class="text-xs text-muted-foreground">({events.length})</span>
      {/if}
    </div>
    {#if onToggle}
      {#if collapsed}
        <ChevronDownIcon class="w-4 h-4 text-muted-foreground" />
      {:else}
        <ChevronUpIcon class="w-4 h-4 text-muted-foreground" />
      {/if}
    {/if}
  </button>

  <!-- Content -->
  {#if !collapsed}
    <div class="max-h-64 overflow-y-auto">
      {#if events.length === 0}
        <div class="px-4 py-8 text-center text-muted-foreground text-sm">
          {emptyMessage}
        </div>
      {:else}
        <div class="divide-y divide-border/50">
          {#each visibleEvents as event (event.id)}
            {@const Icon = getIconComponent(getEventIcon(event.type))}
            <div class="px-4 py-2 hover:bg-muted/30 transition-colors">
              <div class="flex items-start gap-3">
                <!-- Icon -->
                <Icon class="w-4 h-4 mt-0.5 shrink-0 {getEventColor(event.type)}" />

                <!-- Content -->
                <div class="flex-1 min-w-0">
                  <div class="flex items-center gap-2">
                    <span class="text-sm text-foreground truncate">{event.message}</span>
                    {#if showStreamName && event.streamName}
                      <span class="text-xs px-1.5 py-0.5 bg-muted text-muted-foreground truncate max-w-24">
                        {event.streamName}
                      </span>
                    {/if}
                  </div>
                  {#if event.details}
                    <p class="text-xs text-muted-foreground mt-0.5 truncate">{event.details}</p>
                  {/if}
                </div>

                <!-- Timestamp -->
                <div class="text-right shrink-0">
                  <span class="text-xs font-mono text-muted-foreground">{formatTime(event.timestamp)}</span>
                  <span class="text-xs text-muted-foreground/60 block">{formatDate(event.timestamp)}</span>
                </div>
              </div>
            </div>
          {/each}
        </div>

        {#if hasMore}
          <div class="px-4 py-2 border-t border-border/50">
            <Button variant="ghost" size="sm" class="w-full" onclick={loadMore}>
              Load more ({events.length - visibleCount} remaining)
            </Button>
          </div>
        {/if}
      {/if}
    </div>
  {/if}
</div>
