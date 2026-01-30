<script lang="ts">
  import { onMount, onDestroy, tick } from "svelte";
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetConversationStore,
    GetMessagesConnectionStore,
    SendMessageStore,
    LiveMessageReceivedStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { Textarea } from "$lib/components/ui/textarea";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatDistanceToNow, format } from "date-fns";

  // Houdini stores
  const conversationStore = new GetConversationStore();
  const messagesStore = new GetMessagesConnectionStore();
  const sendMessageMutation = new SendMessageStore();
  const messageSubscription = new LiveMessageReceivedStore();

  let isAuthenticated = false;
  let conversationId = $derived($page.params.id ?? "");

  // Conversation data
  let conversation = $derived($conversationStore.data?.conversation);
  let conversationLoading = $derived($conversationStore.fetching);

  // Messages data
  let messagesEdges = $derived($messagesStore.data?.messagesConnection?.edges ?? []);
  let messagesLoading = $derived($messagesStore.fetching);

  // Local messages state (for real-time updates)
  type MessageNode = NonNullable<(typeof messagesEdges)[number]["node"]>;
  let localMessages = $state<MessageNode[]>([]);

  // Sync messages from store to local state
  $effect(() => {
    if (messagesEdges.length > 0) {
      localMessages = messagesEdges.map((e) => e.node).filter((m): m is MessageNode => m != null);
    }
  });

  // Send message state
  let newMessage = $state("");
  let sending = $state(false);

  // Scroll container ref
  let messagesContainer: HTMLDivElement;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  // Handle real-time messages
  $effect(() => {
    const newMsg = $messageSubscription.data?.liveMessageReceived;
    if (newMsg && newMsg.conversationId === conversationId) {
      // Check if message already exists
      const exists = localMessages.some((m) => m.id === newMsg.id);
      if (!exists) {
        localMessages = [...localMessages, newMsg as MessageNode];
        // Scroll to bottom after adding new message
        tick().then(scrollToBottom);
      }
    }
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await Promise.all([loadConversation(), loadMessages()]);

    // Start subscription for real-time messages
    if (conversationId) {
      messageSubscription.listen({ conversationId });
    }

    // Scroll to bottom after loading
    tick().then(scrollToBottom);
  });

  onDestroy(() => {
    messageSubscription.unlisten();
  });

  async function loadConversation() {
    try {
      await conversationStore.fetch({
        variables: { id: conversationId },
        policy: "NetworkOnly",
      });
    } catch (error) {
      console.error("Failed to load conversation:", error);
    }
  }

  async function loadMessages() {
    try {
      await messagesStore.fetch({
        variables: { conversationId, first: 100 },
        policy: "NetworkOnly",
      });
    } catch (error) {
      console.error("Failed to load messages:", error);
    }
  }

  async function sendMessage() {
    if (!newMessage.trim() || sending) return;

    try {
      sending = true;
      const content = newMessage.trim();
      newMessage = ""; // Clear immediately for better UX

      const result = await sendMessageMutation.mutate({
        input: {
          conversationId,
          content,
        },
      });

      const sendResult = result.data?.sendMessage;
      if (sendResult?.__typename === "Message") {
        // Add to local messages if not already added by subscription
        const exists = localMessages.some((m) => m.id === sendResult.id);
        if (!exists) {
          localMessages = [...localMessages, sendResult as MessageNode];
        }
        tick().then(scrollToBottom);
      } else if (sendResult) {
        const errorResult = sendResult as { message?: string };
        toast.error(errorResult.message || "Failed to send message");
        newMessage = content; // Restore message on error
      }
    } catch (error) {
      console.error("Failed to send message:", error);
      toast.error("Failed to send message. Please try again.");
    } finally {
      sending = false;
    }
  }

  function scrollToBottom() {
    if (messagesContainer) {
      messagesContainer.scrollTop = messagesContainer.scrollHeight;
    }
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      sendMessage();
    }
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

  // Icons
  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const SendIcon = getIconComponent("Send");
  const MessageSquareIcon = getIconComponent("MessageSquare");
  const UserIcon = getIconComponent("User");
  const HeadphonesIcon = getIconComponent("Headphones");
</script>

<svelte:head>
  <title>{conversation?.subject || "Conversation"} - Messages - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Header -->
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background"
  >
    <div class="flex items-center gap-4">
      <Button variant="ghost" size="sm" class="gap-2" onclick={() => goto(resolve("/messages"))}>
        <ArrowLeftIcon class="w-4 h-4" />
        Back
      </Button>
      <div class="flex-1 min-w-0">
        <div class="flex items-center gap-2">
          <MessageSquareIcon class="w-5 h-5 text-primary shrink-0" />
          {#if conversationLoading}
            <div class="h-5 bg-muted rounded w-48 animate-pulse"></div>
          {:else}
            <h1 class="text-lg font-bold text-foreground truncate">
              {conversation?.subject || "Conversation"}
            </h1>
            {#if conversation?.status}
              <span
                class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusBadgeClass(
                  conversation.status
                )} shrink-0"
              >
                {conversation.status}
              </span>
            {/if}
          {/if}
        </div>
        {#if conversation?.createdAt}
          <p class="text-xs text-muted-foreground">
            Started {formatDistanceToNow(new Date(conversation.createdAt), { addSuffix: true })}
          </p>
        {/if}
      </div>
    </div>
  </div>

  <!-- Messages Area -->
  <div bind:this={messagesContainer} class="flex-1 overflow-y-auto bg-background/50 p-4 sm:p-6">
    {#if messagesLoading && localMessages.length === 0}
      <!-- Loading skeleton -->
      <div class="space-y-4 max-w-3xl mx-auto">
        {#each Array.from({ length: 4 }) as _, i (i)}
          <div class="flex gap-3 {i % 2 === 0 ? 'justify-end' : ''}">
            {#if i % 2 !== 0}
              <div class="w-8 h-8 rounded-full bg-muted animate-pulse shrink-0"></div>
            {/if}
            <div class="max-w-[70%]">
              <div class="h-16 bg-muted rounded-lg animate-pulse"></div>
            </div>
            {#if i % 2 === 0}
              <div class="w-8 h-8 rounded-full bg-muted animate-pulse shrink-0"></div>
            {/if}
          </div>
        {/each}
      </div>
    {:else if localMessages.length === 0}
      <div class="flex items-center justify-center h-full text-muted-foreground">
        <p>No messages yet. Send the first message!</p>
      </div>
    {:else}
      <div class="space-y-4 max-w-3xl mx-auto">
        {#each localMessages as message (message.id)}
          {@const isUser = message.sender === "USER"}
          {@const isSystem = message.sender === "SYSTEM"}
          {#if isSystem}
            <!-- System/Activity message (centered) -->
            <div class="flex justify-center">
              <p class="text-xs text-muted-foreground italic px-4 py-1">
                {message.content}
                {#if message.createdAt}
                  <span class="ml-2 opacity-70">
                    {format(new Date(message.createdAt), "h:mm a")}
                  </span>
                {/if}
              </p>
            </div>
          {:else}
            <div class="flex gap-3 {isUser ? 'flex-row-reverse' : ''}">
              <!-- Avatar -->
              <div
                class="w-8 h-8 rounded-full shrink-0 flex items-center justify-center {isUser
                  ? 'bg-primary/20'
                  : 'bg-accent/20'}"
              >
                {#if isUser}
                  <UserIcon class="w-4 h-4 text-primary" />
                {:else}
                  <HeadphonesIcon class="w-4 h-4 text-accent" />
                {/if}
              </div>

              <!-- Message bubble -->
              <div class="max-w-[70%] {isUser ? 'text-right' : ''}">
                <div
                  class="inline-block px-4 py-2 rounded-lg {isUser
                    ? 'bg-primary text-primary-foreground'
                    : 'bg-muted text-foreground'}"
                >
                  <p class="text-sm whitespace-pre-wrap break-words text-left">{message.content}</p>
                </div>
                <p
                  class="text-[10px] text-muted-foreground mt-1 {isUser
                    ? 'text-right'
                    : 'text-left'}"
                >
                  {#if message.createdAt}
                    {format(new Date(message.createdAt), "MMM d, h:mm a")}
                  {/if}
                </p>
              </div>
            </div>
          {/if}
        {/each}
      </div>
    {/if}
  </div>

  <!-- Message Input -->
  <div class="border-t border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-4 sm:px-6 shrink-0">
    <div class="max-w-3xl mx-auto">
      <div class="flex gap-3">
        <Textarea
          bind:value={newMessage}
          placeholder="Type your message... (Enter to send, Shift+Enter for new line)"
          rows={2}
          class="flex-1 resize-none"
          onkeydown={handleKeydown}
          disabled={sending}
        />
        <Button onclick={sendMessage} disabled={sending || !newMessage.trim()} class="self-end">
          {#if sending}
            <span
              class="w-4 h-4 border-2 border-current border-t-transparent rounded-full animate-spin"
            ></span>
          {:else}
            <SendIcon class="w-4 h-4" />
          {/if}
          <span class="sr-only">Send</span>
        </Button>
      </div>
      <p class="text-xs text-muted-foreground mt-2">
        Press Enter to send, Shift+Enter for a new line
      </p>
    </div>
  </div>
</div>
