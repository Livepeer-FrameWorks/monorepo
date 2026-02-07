<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    toolName: string;
    payload: Record<string, unknown>;
  }

  let { toolName, payload }: Props = $props();

  let showKey = $state(false);

  const CheckIcon = getIconComponent("Check");
  const XIcon = getIconComponent("X");

  const actionLabels: Record<string, string> = {
    create_stream: "Stream Created",
    update_stream: "Stream Updated",
    delete_stream: "Stream Deleted",
    refresh_stream_key: "Stream Key Refreshed",
  };

  const isError = !!(payload.error || payload.Error);
  const errorMessage = (payload.error || payload.Error || "") as string;

  const fields: { label: string; value: string; masked?: boolean }[] = [];

  function addField(label: string, key: string, masked?: boolean) {
    const value = (payload[key] ?? payload[key.charAt(0).toUpperCase() + key.slice(1)]) as
      | string
      | undefined;
    if (value) fields.push({ label, value: String(value), masked });
  }

  addField("Name", "name");
  addField("Name", "Name");
  addField("Stream ID", "stream_id");
  addField("Stream ID", "StreamID");
  addField("Stream ID", "ID");
  addField("Stream Key", "stream_key", true);
  addField("Stream Key", "StreamKey", true);
  addField("Playback ID", "playback_id");
  addField("Playback ID", "PlaybackID");
  addField("Message", "message");
  addField("Message", "Message");

  // Deduplicate by label (first wins)
  const uniqueFields = fields.filter(
    (field, index, arr) => arr.findIndex((f) => f.label === field.label) === index
  );
</script>

<div class="rounded-lg border border-border bg-card text-sm">
  <div class="flex items-center gap-2 border-b border-border px-4 py-2.5">
    {#if isError}
      <div class="flex h-5 w-5 items-center justify-center rounded-full bg-red-500/10 text-red-500">
        <XIcon class="h-3 w-3" />
      </div>
      <span class="font-semibold text-foreground">
        {actionLabels[toolName] ?? toolName} — Failed
      </span>
    {:else}
      <div
        class="flex h-5 w-5 items-center justify-center rounded-full bg-emerald-500/10 text-emerald-500"
      >
        <CheckIcon class="h-3 w-3" />
      </div>
      <span class="font-semibold text-foreground">
        {actionLabels[toolName] ?? toolName}
      </span>
    {/if}
  </div>

  {#if isError}
    <div class="px-4 py-2.5">
      <p class="text-xs text-red-500">{errorMessage}</p>
    </div>
  {:else if uniqueFields.length > 0}
    <div class="divide-y divide-border">
      {#each uniqueFields as field (field.label)}
        <div class="flex items-center justify-between gap-4 px-4 py-2">
          <span class="text-xs text-muted-foreground">{field.label}</span>
          {#if field.masked}
            <div class="flex items-center gap-2">
              <span class="font-mono text-xs text-foreground">
                {showKey ? field.value : "\u25CF".repeat(8)}
              </span>
              <button
                type="button"
                class="text-[10px] text-primary hover:text-primary/80"
                onclick={() => (showKey = !showKey)}
              >
                {showKey ? "Hide" : "Show"}
              </button>
            </div>
          {:else}
            <span class="truncate font-mono text-xs text-foreground">{field.value}</span>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>
