<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    onSend?: (message: string) => void;
  }

  let { onSend }: Props = $props();

  const categories = [
    {
      label: "Diagnostics",
      icon: "Activity",
      description: "Rebuffering, latency, packet loss, health checks",
      prompt: "Why are my viewers rebuffering?",
    },
    {
      label: "Streams",
      icon: "Radio",
      description: "Create, update, delete streams, refresh keys",
      prompt: "Create a new test stream",
    },
    {
      label: "Analytics",
      icon: "BarChart3",
      description: "Stream health summaries, anomaly reports",
      prompt: "Show me my stream health summary",
    },
    {
      label: "Media",
      icon: "Film",
      description: "Clips, DVR recordings, VOD management",
      prompt: "Create a clip from my stream",
    },
    {
      label: "Knowledge",
      icon: "BookOpen",
      description: "Search docs, guides, and the web",
      prompt: "How do I set up SRT ingest?",
    },
    {
      label: "Support",
      icon: "MessageCircle",
      description: "Search past support tickets and history",
      prompt: "Search my past support tickets",
    },
  ];

  const BotIcon = getIconComponent("Bot");
</script>

<div class="flex h-full flex-col items-center justify-center px-6">
  <div class="mb-8 text-center">
    <div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10">
      <BotIcon class="h-7 w-7 text-primary" />
    </div>
    <h2 class="text-lg font-semibold text-foreground">Skipper</h2>
    <p class="mt-1 text-sm text-muted-foreground">
      Your AI video consultant. Ask about diagnostics, streams, media, or anything else.
    </p>
  </div>

  <div class="grid w-full max-w-2xl grid-cols-2 gap-3 sm:grid-cols-3">
    {#each categories as category (category.label)}
      {@const IconComponent = getIconComponent(category.icon)}
      <button
        type="button"
        class="group rounded-xl border border-border bg-card px-4 py-3 text-left transition hover:border-primary/30 hover:bg-primary/5"
        onclick={() => onSend?.(category.prompt)}
      >
        <div class="mb-1.5 flex items-center gap-2">
          <IconComponent
            class="h-4 w-4 text-muted-foreground transition group-hover:text-primary"
          />
          <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
            {category.label}
          </span>
        </div>
        <p class="text-sm font-medium text-foreground">{category.prompt}</p>
        <p class="mt-1 text-[11px] text-muted-foreground/70">{category.description}</p>
      </button>
    {/each}
  </div>

  <p class="mt-6 max-w-md text-center text-xs text-muted-foreground/60">
    Skipper can also manage DVR recordings, check billing, explore the GraphQL API, and more — just
    ask.
  </p>
</div>
