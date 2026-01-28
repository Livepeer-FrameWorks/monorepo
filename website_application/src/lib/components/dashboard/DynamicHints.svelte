<script lang="ts">
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    hasStreams?: boolean;
    hasLiveStreams?: boolean;
    streamKey?: string | null;
  }

  let { hasStreams = false, hasLiveStreams = false, streamKey = null }: Props = $props();

  interface Hint {
    id: string;
    icon: string;
    iconColor: string;
    title: string;
    getDescription: () => string;
    link?: string;
    linkText?: string;
    condition: () => boolean;
    priority: number;
  }

  const hints: Hint[] = [
    {
      id: "create-stream",
      icon: "Video",
      iconColor: "text-primary",
      title: "Create Your First Stream",
      getDescription: () =>
        "Set up a stream to start broadcasting. You'll get RTMP, SRT, and WHIP ingest URLs.",
      link: "/streams",
      linkText: "Create Stream",
      condition: () => !hasStreams,
      priority: 100,
    },
    {
      id: "obs-setup",
      icon: "Settings2",
      iconColor: "text-info",
      title: "OBS Setup",
      getDescription: () =>
        streamKey
          ? `Use stream key: ${streamKey.slice(0, 8)}... with RTMP ingest.`
          : "Configure OBS or your preferred encoder with your stream key.",
      link: "/streams",
      linkText: "View Keys",
      condition: () => hasStreams && !hasLiveStreams,
      priority: 80,
    },
    {
      id: "mcp-integration",
      icon: "Bot",
      iconColor: "text-success",
      title: "AI Agent Integration",
      getDescription: () => "Manage streams via Claude, Cursor, or any MCP-compatible AI client.",
      link: "/developer/sdks",
      linkText: "View Setup",
      condition: () => true,
      priority: 50,
    },
    {
      id: "player-sdk",
      icon: "Play",
      iconColor: "text-destructive",
      title: "Embed Your Stream",
      getDescription: () =>
        "Use the Player SDK to embed streams in your React, Svelte, or vanilla JS app.",
      link: "/developer/sdks",
      linkText: "View SDKs",
      condition: () => hasStreams,
      priority: 40,
    },
    {
      id: "analytics",
      icon: "ChartLine",
      iconColor: "text-accent-purple",
      title: "Track Your Audience",
      getDescription: () => "View real-time analytics, geographic distribution, and viewer trends.",
      link: "/analytics",
      linkText: "View Analytics",
      condition: () => hasStreams,
      priority: 30,
    },
    {
      id: "graphql-api",
      icon: "Code2",
      iconColor: "text-warning",
      title: "GraphQL API",
      getDescription: () => "Query and mutate your data programmatically with the GraphQL API.",
      link: "/developer/api",
      linkText: "Explore API",
      condition: () => true,
      priority: 20,
    },
  ];

  // Get applicable hints sorted by priority
  let applicableHints = $derived(
    hints.filter((h) => h.condition()).sort((a, b) => b.priority - a.priority)
  );

  // Current hint index (cycles through)
  let currentIndex = $state(0);

  // Get stored dismissed hints from localStorage
  let dismissedHints = $state<string[]>([]);

  // Filter out dismissed hints
  let visibleHints = $derived(applicableHints.filter((h) => !dismissedHints.includes(h.id)));

  let currentHint = $derived(visibleHints[currentIndex % visibleHints.length] || null);

  function nextHint() {
    if (visibleHints.length > 1) {
      currentIndex = (currentIndex + 1) % visibleHints.length;
    }
  }

  function dismissHint() {
    if (currentHint) {
      dismissedHints = [...dismissedHints, currentHint.id];
      // Save to localStorage
      if (typeof window !== "undefined") {
        localStorage.setItem("dismissedHints", JSON.stringify(dismissedHints));
      }
    }
  }

  // Load dismissed hints from localStorage on mount
  $effect(() => {
    if (typeof window !== "undefined") {
      const stored = localStorage.getItem("dismissedHints");
      if (stored) {
        try {
          dismissedHints = JSON.parse(stored);
        } catch {
          dismissedHints = [];
        }
      }
    }
  });

  // Auto-cycle hints every 10 seconds
  let cycleInterval: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    if (visibleHints.length > 1) {
      cycleInterval = setInterval(nextHint, 10000);
    }
    return () => {
      if (cycleInterval) clearInterval(cycleInterval);
    };
  });
</script>

{#if currentHint}
  {@const Icon = getIconComponent(currentHint.icon)}
  {@const XIcon = getIconComponent("X")}
  {@const ChevronRightIcon = getIconComponent("ChevronRight")}
  {@const LightbulbIcon = getIconComponent("Lightbulb")}

  <div class="bg-muted/20 border-y border-border/30 px-4 py-2.5">
    <div class="flex items-center gap-3">
      <LightbulbIcon class="w-4 h-4 text-warning shrink-0" />
      <Icon class="w-4 h-4 {currentHint.iconColor} shrink-0" />
      <span class="text-foreground font-medium text-sm">{currentHint.title}</span>
      <span class="text-muted-foreground text-sm hidden sm:inline">—</span>
      <span class="text-muted-foreground text-sm hidden sm:inline flex-1 truncate"
        >{currentHint.getDescription()}</span
      >
      {#if currentHint.link}
        <a href={resolve(currentHint.link)} class="text-primary hover:underline text-sm shrink-0">
          {currentHint.linkText} →
        </a>
      {/if}
      <div class="flex items-center gap-1 shrink-0 ml-auto sm:ml-0">
        {#if visibleHints.length > 1}
          <span class="text-[10px] text-muted-foreground">
            {(currentIndex % visibleHints.length) + 1}/{visibleHints.length}
          </span>
          <button
            type="button"
            class="p-1 text-muted-foreground hover:text-foreground transition-colors rounded"
            onclick={nextHint}
            title="Next tip"
          >
            <ChevronRightIcon class="w-4 h-4" />
          </button>
        {/if}
        <button
          type="button"
          class="p-1 text-muted-foreground hover:text-foreground transition-colors rounded"
          onclick={dismissHint}
          title="Dismiss"
        >
          <XIcon class="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  </div>
{/if}
