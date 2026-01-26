<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetConversationsConnectionStore,
    CreateConversationStore,
    LiveConversationUpdatesStore
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDistanceToNow } from "date-fns";

  // Houdini stores
  const conversationsStore = new GetConversationsConnectionStore();
  const createConversationMutation = new CreateConversationStore();
  const conversationUpdates = new LiveConversationUpdatesStore();

  let isAuthenticated = false;

  // State from Houdini
  let conversations = $derived($conversationsStore.data?.conversationsConnection?.edges ?? []);
  let loading = $derived($conversationsStore.fetching);
  let totalCount = $derived($conversationsStore.data?.conversationsConnection?.totalCount ?? 0);

  // Computed stats
  let openCount = $derived(conversations.filter(c => c.node?.status === "OPEN").length);
  let resolvedCount = $derived(conversations.filter(c => c.node?.status === "RESOLVED").length);
  let pendingCount = $derived(conversations.filter(c => c.node?.status === "PENDING").length);

  // Create conversation modal
  let showCreateModal = $state(false);
  let creating = $state(false);
  let newSubject = $state("");
  let newMessage = $state("");

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadConversations();

    // Subscribe to conversation updates for real-time list refresh
    conversationUpdates.listen({});
  });

  onDestroy(() => {
    conversationUpdates.unlisten();
  });

  $effect(() => {
    if ($conversationUpdates.data?.liveConversationUpdates) {
      loadConversations();
    }
  });

  async function loadConversations() {
    try {
      await conversationsStore.fetch({
        variables: { first: 50 },
        policy: "NetworkOnly",
      });
    } catch (error) {
      console.error("Failed to load conversations:", error);
    }
  }

  async function createConversation() {
    if (!newMessage.trim()) {
      toast.warning("Please enter a message");
      return;
    }

    try {
      creating = true;
      const result = await createConversationMutation.mutate({
        input: {
          subject: newSubject.trim() || undefined,
          message: newMessage.trim(),
          pageUrl: window.location.href,
        },
      });

      const createResult = result.data?.createConversation;
      if (createResult?.__typename === "Conversation") {
        // Close modal and reset form
        showCreateModal = false;
        newSubject = "";
        newMessage = "";
        toast.success("Conversation started!");

        // Navigate to the new conversation
        goto(resolve(`/messages/${createResult.id}`));
      } else if (createResult) {
        const errorResult = createResult as { message?: string };
        toast.error(errorResult.message || "Failed to start conversation");
      }
    } catch (error) {
      console.error("Failed to create conversation:", error);
      toast.error("Failed to start conversation. Please try again.");
    } finally {
      creating = false;
    }
  }

  function navigateToConversation(id: string) {
    goto(resolve(`/messages/${id}`));
  }

  function getStatusBadgeClass(status: string | null | undefined): string {
    switch (status) {
      case "OPEN":
        return "bg-primary/10 text-primary border-primary/20";
      case "RESOLVED":
        return "bg-success/10 text-success border-success/20";
      case "PENDING":
        return "bg-warning/10 text-warning border-warning/20";
      default:
        return "bg-muted text-muted-foreground border-border";
    }
  }

  function truncateMessage(content: string | undefined, maxLength: number = 100): string {
    if (!content) return "";
    if (content.length <= maxLength) return content;
    return content.substring(0, maxLength) + "...";
  }

  // Icons
  const MessageSquareIcon = getIconComponent("MessageSquare");
  const PlusIcon = getIconComponent("Plus");
  const InboxIcon = getIconComponent("Inbox");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const ClockIcon = getIconComponent("Clock");
  const ChevronRightIcon = getIconComponent("ChevronRight");
</script>

<svelte:head>
  <title>Messages - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <MessageSquareIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Messages</h1>
          <p class="text-sm text-muted-foreground">
            Contact support and view conversation history
          </p>
        </div>
      </div>
      <Button
        variant="outline"
        size="sm"
        class="gap-2"
        onclick={() => (showCreateModal = true)}
      >
        <PlusIcon class="w-4 h-4" />
        New Conversation
      </Button>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto bg-background/50">
    {#if loading}
      <!-- Loading Skeleton -->
      <GridSeam cols={3} stack="2x2" flush={true} class="min-h-full content-start">
        {#each Array.from({ length: 6 }) as _, i (i)}
          <div class="slab h-full !p-0">
            <div class="slab-header">
              <div class="h-4 bg-muted rounded w-3/4 animate-pulse"></div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-3">
                <div class="h-4 bg-muted rounded w-full animate-pulse"></div>
                <div class="h-4 bg-muted rounded w-1/2 animate-pulse"></div>
              </div>
            </div>
          </div>
        {/each}
      </GridSeam>
    {:else}
      <div class="page-transition">
        <!-- Stats Bar -->
        <GridSeam cols={3} stack="2x2" surface="panel" flush={true} class="mb-0 min-h-full content-start">
          <div>
            <DashboardMetricCard
              icon={InboxIcon}
              iconColor="text-primary"
              value={openCount}
              valueColor="text-primary"
              label="Open"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ClockIcon}
              iconColor="text-warning"
              value={pendingCount}
              valueColor="text-warning"
              label="Pending"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={CheckCircleIcon}
              iconColor="text-success"
              value={resolvedCount}
              valueColor="text-success"
              label="Resolved"
            />
          </div>
        </GridSeam>

        <!-- Conversations List -->
        <div class="dashboard-grid">
          <div class="slab col-span-full">
            <div class="slab-header flex justify-between items-center">
              <div class="flex items-center gap-2">
                <MessageSquareIcon class="w-4 h-4 text-info" />
                <h3>Conversations ({totalCount})</h3>
              </div>
            </div>
            <div class="slab-body--flush">
              {#if conversations.length === 0}
                <div class="p-8">
                  <EmptyState
                    iconName="MessageSquare"
                    title="No conversations yet"
                    description="Start a conversation with our support team for help with any questions."
                    actionText="Start Conversation"
                    onAction={() => (showCreateModal = true)}
                  />
                </div>
              {:else}
                <div class="divide-y divide-border">
                  {#each conversations as edge (edge.cursor)}
                    {@const conv = edge.node}
                    {#if conv}
                      <button
                        type="button"
                        class="w-full text-left px-4 py-4 hover:bg-muted/50 transition-colors flex items-start gap-4 group"
                        onclick={() => navigateToConversation(conv.id)}
                      >
                        <!-- Status indicator -->
                        <div class="shrink-0 mt-1">
                          {#if conv.status === "OPEN"}
                            <div class="w-2 h-2 rounded-full bg-primary"></div>
                          {:else if conv.status === "PENDING"}
                            <div class="w-2 h-2 rounded-full bg-warning"></div>
                          {:else}
                            <div class="w-2 h-2 rounded-full bg-muted-foreground"></div>
                          {/if}
                        </div>

                        <!-- Content -->
                        <div class="flex-1 min-w-0">
                          <div class="flex items-center gap-2 mb-1">
                            <span class="font-medium text-foreground truncate">
                              {conv.subject || "New conversation"}
                            </span>
                            <span class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusBadgeClass(conv.status)}">
                              {conv.status}
                            </span>
                            {#if conv.unreadCount > 0}
                              <span class="inline-flex items-center justify-center w-5 h-5 rounded-full bg-primary text-primary-foreground text-[10px] font-bold">
                                {conv.unreadCount}
                              </span>
                            {/if}
                          </div>
                          {#if conv.lastMessage}
                            <p class="text-sm text-muted-foreground truncate">
                              {#if conv.lastMessage.sender === "AGENT"}
                                <span class="text-primary">Support:</span>
                              {:else}
                                <span class="text-foreground">You:</span>
                              {/if}
                              {truncateMessage(conv.lastMessage.content)}
                            </p>
                          {/if}
                          <p class="text-xs text-muted-foreground mt-1">
                            {formatDistanceToNow(new Date(conv.updatedAt), { addSuffix: true })}
                          </p>
                        </div>

                        <!-- Arrow -->
                        <ChevronRightIcon class="w-5 h-5 text-muted-foreground group-hover:text-foreground transition-colors shrink-0" />
                      </button>
                    {/if}
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

<!-- Create Conversation Modal -->
<Dialog bind:open={showCreateModal}>
  <DialogContent class="sm:max-w-md">
    <DialogHeader>
      <DialogTitle>Start a Conversation</DialogTitle>
      <DialogDescription>
        Send a message to our support team. We'll get back to you as soon as possible.
      </DialogDescription>
    </DialogHeader>
    <div class="grid gap-4 py-4">
      <div class="grid gap-2">
        <label for="subject" class="text-sm font-medium">Subject (optional)</label>
        <Input
          id="subject"
          bind:value={newSubject}
          placeholder="What's this about?"
        />
      </div>
      <div class="grid gap-2">
        <label for="message" class="text-sm font-medium">Message</label>
        <Textarea
          id="message"
          bind:value={newMessage}
          placeholder="How can we help you?"
          rows={4}
        />
      </div>
    </div>
    <DialogFooter>
      <Button variant="outline" onclick={() => (showCreateModal = false)}>
        Cancel
      </Button>
      <Button onclick={createConversation} disabled={creating || !newMessage.trim()}>
        {#if creating}
          Sending...
        {:else}
          Send Message
        {/if}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
