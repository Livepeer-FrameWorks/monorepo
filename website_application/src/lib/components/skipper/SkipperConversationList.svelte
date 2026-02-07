<script lang="ts">
  import { formatDistanceToNow } from "date-fns";
  import { getIconComponent } from "$lib/iconUtils";

  export interface SkipperConversationSummary {
    ID: string;
    Title: string;
    MessageCount: number;
    UpdatedAt: string;
    LastMessageAt?: string | null;
  }

  interface Props {
    conversations: SkipperConversationSummary[];
    activeId?: string;
    onSelect?: (id: string) => void;
    onNew?: () => void;
    onDelete?: (id: string) => void;
    onRename?: (id: string, title: string) => void;
  }

  let { conversations, activeId = "", onSelect, onNew, onDelete, onRename }: Props = $props();

  let menuOpenId = $state<string | null>(null);
  let editingId = $state<string | null>(null);
  let editTitle = $state("");

  const PlusIcon = getIconComponent("Plus");
  const MessageCircleIcon = getIconComponent("MessageCircle");
  const BotIcon = getIconComponent("Bot");
  const MoreHorizontalIcon = getIconComponent("MoreHorizontal");
  const PencilIcon = getIconComponent("Pencil");
  const Trash2Icon = getIconComponent("Trash2");
  const CheckIcon = getIconComponent("Check");
  const XIcon = getIconComponent("X");

  function toggleMenu(id: string, event: MouseEvent) {
    event.stopPropagation();
    menuOpenId = menuOpenId === id ? null : id;
  }

  function startRename(convo: SkipperConversationSummary, event: MouseEvent) {
    event.stopPropagation();
    editingId = convo.ID;
    editTitle = convo.Title || "";
    menuOpenId = null;
  }

  function confirmRename(event: MouseEvent) {
    event.stopPropagation();
    if (editingId && editTitle.trim()) {
      onRename?.(editingId, editTitle.trim());
    }
    editingId = null;
    editTitle = "";
  }

  function cancelRename(event: MouseEvent) {
    event.stopPropagation();
    editingId = null;
    editTitle = "";
  }

  function handleRenameKeydown(event: KeyboardEvent) {
    if (event.key === "Enter") {
      event.preventDefault();
      if (editingId && editTitle.trim()) {
        onRename?.(editingId, editTitle.trim());
      }
      editingId = null;
      editTitle = "";
    } else if (event.key === "Escape") {
      editingId = null;
      editTitle = "";
    }
  }

  function handleDelete(id: string, event: MouseEvent) {
    event.stopPropagation();
    menuOpenId = null;
    onDelete?.(id);
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div class="flex h-full flex-col" onclick={() => (menuOpenId = null)}>
  <div class="flex items-center justify-between border-b border-border px-4 py-3">
    <span class="text-xs font-semibold uppercase tracking-[0.2em] text-muted-foreground">
      Conversations
    </span>
    <button
      class="rounded-md border border-border bg-background p-1.5 text-muted-foreground transition hover:bg-muted hover:text-foreground"
      onclick={() => onNew?.()}
      aria-label="New conversation"
    >
      <PlusIcon class="h-3.5 w-3.5" />
    </button>
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if conversations.length === 0}
      <div class="px-4 py-8 text-center">
        <BotIcon class="mx-auto mb-2 h-8 w-8 text-muted-foreground/40" />
        <p class="text-sm text-muted-foreground">No conversations yet</p>
        <button class="mt-2 text-xs text-primary hover:underline" onclick={() => onNew?.()}>
          Start your first conversation
        </button>
      </div>
    {:else}
      <div class="space-y-0.5 p-2">
        {#each conversations as convo (convo.ID)}
          <div class="group relative">
            <button
              type="button"
              class="w-full rounded-lg px-3 py-2.5 text-left transition {convo.ID === activeId
                ? 'bg-primary/10 text-foreground'
                : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground'}"
              onclick={() => onSelect?.(convo.ID)}
            >
              <div class="flex items-start gap-2">
                <MessageCircleIcon
                  class="mt-0.5 h-3.5 w-3.5 shrink-0 {convo.ID === activeId
                    ? 'text-primary'
                    : 'text-muted-foreground/50'}"
                />
                <div class="min-w-0 flex-1">
                  {#if editingId === convo.ID}
                    <!-- svelte-ignore a11y_autofocus -->
                    <div class="flex items-center gap-1">
                      <input
                        type="text"
                        bind:value={editTitle}
                        onkeydown={handleRenameKeydown}
                        onclick={(e) => e.stopPropagation()}
                        autofocus
                        class="w-full rounded border border-primary bg-background px-1.5 py-0.5 text-sm text-foreground outline-none"
                      />
                      <button
                        type="button"
                        class="rounded p-0.5 text-emerald-500 hover:bg-emerald-500/10"
                        onclick={confirmRename}
                        aria-label="Confirm rename"
                      >
                        <CheckIcon class="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        class="rounded p-0.5 text-muted-foreground hover:bg-muted"
                        onclick={cancelRename}
                        aria-label="Cancel rename"
                      >
                        <XIcon class="h-3.5 w-3.5" />
                      </button>
                    </div>
                  {:else}
                    <p class="truncate pr-6 text-sm font-medium">
                      {convo.Title || "New conversation"}
                    </p>
                  {/if}
                  <div class="mt-0.5 flex items-center gap-2 text-[11px] text-muted-foreground">
                    <span>
                      {formatDistanceToNow(new Date(convo.LastMessageAt || convo.UpdatedAt), {
                        addSuffix: true,
                      })}
                    </span>
                    {#if convo.MessageCount > 0}
                      <span class="text-muted-foreground/40">&middot;</span>
                      <span>{convo.MessageCount} msgs</span>
                    {/if}
                  </div>
                </div>
              </div>
            </button>

            {#if editingId !== convo.ID}
              <button
                type="button"
                class="absolute right-2 top-2.5 rounded p-1 text-muted-foreground/50 opacity-0 transition hover:bg-muted hover:text-foreground group-hover:opacity-100
                  {menuOpenId === convo.ID ? 'opacity-100' : ''}"
                onclick={(e) => toggleMenu(convo.ID, e)}
                aria-label="Conversation options"
              >
                <MoreHorizontalIcon class="h-3.5 w-3.5" />
              </button>
            {/if}

            {#if menuOpenId === convo.ID}
              <div
                class="absolute right-2 top-9 z-10 min-w-[120px] rounded-lg border border-border bg-popover p-1 shadow-md"
              >
                <button
                  type="button"
                  class="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-xs text-foreground hover:bg-muted"
                  onclick={(e) => startRename(convo, e)}
                >
                  <PencilIcon class="h-3 w-3" />
                  Rename
                </button>
                <button
                  type="button"
                  class="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-xs text-red-500 hover:bg-red-500/10"
                  onclick={(e) => handleDelete(convo.ID, e)}
                >
                  <Trash2Icon class="h-3 w-3" />
                  Delete
                </button>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>
