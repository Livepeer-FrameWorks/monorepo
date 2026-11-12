<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { Monitor, Terminal, Laptop, ShieldCheck } from "lucide-svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogDescription,
    DialogFooter,
  } from "$lib/components/ui/dialog";
  import { auth } from "$lib/stores/auth";
  import { toast } from "$lib/stores/toast";
  import SecuritySignInMethods from "$lib/components/security/SecuritySignInMethods.svelte";
  import {
    loadSecuritySessions,
    loadSecurityActivity,
    revokeSecuritySession,
    revokeOtherSecuritySessions,
    renameSecuritySession,
    securityTimestamp,
    securityMethodLabels,
    securityActivityLabels,
    type SecuritySession,
    type SecurityActivity,
  } from "$lib/account-security";

  let sessions = $state<SecuritySession[]>([]);
  let activity = $state<SecurityActivity[]>([]);
  let sessionsLoading = $state(true);
  let activityLoading = $state(true);
  let sessionsError = $state("");
  let activityError = $state("");
  let sessionCursor = $state<string | null>(null);
  let activityCursor = $state<string | null>(null);
  let moreSessions = $state(false);
  let moreActivity = $state(false);
  let sessionTotal = $state(0);
  let busy = $state(false);
  let actionError = $state("");
  let label = $state("");
  type Action = { kind: "rename" | "revoke"; session: SecuritySession } | { kind: "others" };
  let action = $state<Action | null>(null);
  let sessionRequest = 0;
  let activityRequest = 0;

  function errorMessage(error: unknown) {
    return error instanceof Error ? error.message : "The security request failed. Try again.";
  }
  async function readSessions(append = false) {
    const request = ++sessionRequest;
    sessionsLoading = true;
    sessionsError = "";
    try {
      const page = await loadSecuritySessions(append ? sessionCursor : null);
      if (request !== sessionRequest) return;
      const rows = append ? [...sessions, ...page.nodes] : page.nodes;
      sessions = [...new Map(rows.map((row) => [row.id, row])).values()];
      sessionCursor = page.pageInfo.endCursor;
      moreSessions = page.pageInfo.hasNextPage;
      sessionTotal = page.totalCount;
    } catch (error) {
      if (request === sessionRequest) sessionsError = errorMessage(error);
    } finally {
      if (request === sessionRequest) sessionsLoading = false;
    }
  }
  async function readActivity(append = false) {
    const request = ++activityRequest;
    activityLoading = true;
    activityError = "";
    try {
      const page = await loadSecurityActivity(append ? activityCursor : null);
      if (request !== activityRequest) return;
      const rows = append ? [...activity, ...page.nodes] : page.nodes;
      activity = [...new Map(rows.map((row) => [row.id, row])).values()];
      activityCursor = page.pageInfo.endCursor;
      moreActivity = page.pageInfo.hasNextPage;
    } catch (error) {
      if (request === activityRequest) activityError = errorMessage(error);
    } finally {
      if (request === activityRequest) activityLoading = false;
    }
  }
  onMount(() => {
    void readSessions();
    void readActivity();
  });
  onDestroy(() => {
    sessionRequest++;
    activityRequest++;
  });
  function selectAction(next: Action) {
    action = next;
    label = next.kind === "rename" ? next.session.label : "";
    actionError = "";
  }
  function cancelAction() {
    if (!busy) {
      action = null;
      label = "";
      actionError = "";
    }
  }
  const actionTitle = $derived(
    action?.kind === "rename"
      ? "Rename session"
      : action?.kind === "others"
        ? "Sign out all other sessions"
        : "Sign out session"
  );
  async function confirmAction() {
    const selected = action;
    if (!selected || busy) return;
    busy = true;
    actionError = "";
    try {
      if (selected.kind === "rename") {
        if (!label.trim()) throw new Error("Enter a session name.");
        await renameSecuritySession(selected.session.id, label.trim());
        toast.success("Session renamed");
      } else if (selected.kind === "others") {
        const count = await revokeOtherSecuritySessions();
        toast.success(`Signed out ${count} other session${count === 1 ? "" : "s"}`);
      } else if (selected.session.current) {
        const result = await auth.logout();
        if (!result.success) throw new Error(result.error || "Could not sign out. Try again.");
        await goto(resolve("/login"));
        return;
      } else {
        const current = await revokeSecuritySession(selected.session.id);
        if (current) {
          auth.clearLocalSession();
          await goto(resolve("/login"));
          return;
        }
        toast.success("Session signed out");
      }
      action = null;
      label = "";
      await readSessions();
      void readActivity();
    } catch (error) {
      actionError = errorMessage(error);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Account Security - FrameWorks</title></svelte:head>
<div class="h-full flex flex-col">
  <header class="px-6 py-5 border-b border-border">
    <div class="flex items-center gap-3">
      <ShieldCheck class="w-6 h-6 text-primary" />
      <h1 class="text-2xl font-semibold">Security</h1>
    </div>
    <p class="mt-2 text-sm text-muted-foreground">
      Manage your own sign-ins, password and linked wallets.
    </p>
  </header>
  <div class="slab">
    <div class="slab-header flex items-center justify-between gap-3">
      <h2>Signed-in sessions <span class="text-muted-foreground">({sessionTotal})</span></h2>
      <Button
        variant="ghost"
        size="sm"
        onclick={() => selectAction({ kind: "others" })}
        disabled={busy || sessionsLoading || sessions.length === 0}
        >Sign out all other sessions</Button
      >
    </div>
    <div class="slab-body--padded text-sm text-muted-foreground">
      Access credentials renew automatically. Browser sessions last at most 90 days; CLI and tray
      sessions last at most 180 days, with a 30-day inactivity limit. Last active records a refresh.
    </div>
    {#if sessionsError}<div class="slab-body--padded">
        <p role="alert" class="text-sm text-destructive">{sessionsError}</p>
      </div>{/if}
    {#if sessionsLoading && sessions.length === 0}<p
        class="slab-body--padded text-muted-foreground"
        role="status"
      >
        Loading signed-in sessions...
      </p>
    {:else if sessions.length === 0 && !sessionsError}<p
        class="slab-body--padded text-muted-foreground"
      >
        No active sessions were returned. Refresh to check again.
      </p>{/if}
    <div class="overflow-x-auto">
      <table class="w-full text-sm text-left">
        <caption class="sr-only">Your signed-in browser, CLI and desktop sessions</caption>
        <thead class="border-y border-border text-muted-foreground"
          ><tr
            ><th class="px-5 py-3">Session</th><th class="px-5 py-3">Location</th><th
              class="px-5 py-3">Times</th
            ><th class="px-5 py-3">Actions</th></tr
          ></thead
        >
        <tbody
          >{#each sessions as session (session.id)}
            <tr class="border-b border-border align-top">
              <td class="px-5 py-4"
                ><div class="flex items-center gap-2">
                  {#if session.clientKind === "CLI"}<Terminal
                      class="w-4 h-4 shrink-0"
                    />{:else if session.clientKind === "DESKTOP"}<Laptop
                      class="w-4 h-4 shrink-0"
                    />{:else}<Monitor class="w-4 h-4 shrink-0" />{/if}
                  <span class="font-medium">{session.label || "Unnamed session"}</span>
                  {#if session.current}<span
                      class="border border-primary/40 px-2 py-0.5 text-xs text-primary"
                      >This session</span
                    >{/if}
                </div>
                <p class="mt-1 text-xs text-muted-foreground">
                  {securityMethodLabels[session.authMethod]} · {session.clientId}
                </p>
                {#if session.clientVersion}<p class="mt-1 text-xs text-muted-foreground">
                    {session.clientVersion}
                  </p>{/if}</td
              >
              <td class="px-5 py-4"
                ><p>{session.location || "Location unavailable"}</p>
                <p class="mt-1 font-mono text-xs text-muted-foreground">
                  {session.ipAddress || "Address unavailable"}
                </p></td
              >
              <td class="px-5 py-4 text-xs text-muted-foreground"
                ><p>Last active {securityTimestamp(session.lastActiveAt)}</p>
                <p class="mt-1">Signed in {securityTimestamp(session.createdAt)}</p>
                <p class="mt-1">Expires {securityTimestamp(session.expiresAt)}</p></td
              >
              <td class="px-5 py-4"
                ><div class="flex flex-wrap gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    aria-label={`Rename ${session.label || "session"}`}
                    disabled={busy}
                    onclick={() => selectAction({ kind: "rename", session })}>Rename</Button
                  ><Button
                    variant="outline"
                    size="sm"
                    aria-label={`Sign out ${session.label || "session"}`}
                    disabled={busy}
                    onclick={() => selectAction({ kind: "revoke", session })}>Sign out</Button
                  >
                </div></td
              >
            </tr>
          {/each}</tbody
        >
      </table>
    </div>
    <div class="slab-actions slab-actions--row">
      <Button variant="ghost" disabled={sessionsLoading} onclick={() => readSessions()}
        >Refresh sessions</Button
      >{#if moreSessions}<Button
          variant="ghost"
          disabled={sessionsLoading}
          onclick={() => readSessions(true)}>Load more sessions</Button
        >{/if}
    </div>
  </div>
  <SecuritySignInMethods
    onSecurityChange={() => {
      void readSessions();
      void readActivity();
    }}
  />
  <div class="slab">
    <div class="slab-header"><h2>Recent security activity</h2></div>
    <p class="slab-body--padded text-sm text-muted-foreground">
      Your own events from the last 90 days. New events can take a short time to appear. Security
      emails for unfamiliar devices and detected token theft are always enabled for verified linked
      addresses.
    </p>
    {#if activityError}<div class="slab-body--padded">
        <p role="alert" class="text-sm text-destructive">{activityError}</p>
      </div>{/if}
    {#if activityLoading && activity.length === 0}<p
        class="slab-body--padded text-muted-foreground"
        role="status"
      >
        Loading security activity...
      </p>{:else if activity.length === 0 && !activityError}<p
        class="slab-body--padded text-muted-foreground"
      >
        No recent security activity.
      </p>{/if}
    <ul class="divide-y divide-border">
      {#each activity as event (event.id)}<li class="px-5 py-3 text-sm">
          <p class="font-medium">{securityActivityLabels[event.kind]}</p>
          <p class="mt-1 text-xs text-muted-foreground">
            {securityTimestamp(event.timestamp)} · {securityMethodLabels[event.authMethod]}
          </p>
          {#if event.reason}<p class="mt-1 text-xs text-muted-foreground">
              {event.reason.replaceAll("_", " ")}
            </p>{/if}
        </li>{/each}
    </ul>
    <div class="slab-actions slab-actions--row">
      <Button variant="ghost" disabled={activityLoading} onclick={() => readActivity()}
        >Refresh activity</Button
      >{#if moreActivity}<Button
          variant="ghost"
          disabled={activityLoading}
          onclick={() => readActivity(true)}>Load more activity</Button
        >{/if}
    </div>
  </div>
</div>

<Dialog
  open={action !== null}
  onOpenChange={(open) => {
    if (!open) cancelAction();
  }}
>
  <DialogContent
    class="max-w-md rounded-none border-border bg-background p-0 gap-0 overflow-hidden"
  >
    <form
      onsubmit={(event) => {
        event.preventDefault();
        void confirmAction();
      }}
    >
      <DialogHeader class="slab-header text-left"
        ><DialogTitle>{actionTitle}</DialogTitle><DialogDescription
          >{action?.kind === "rename"
            ? "Choose a name that helps you recognise this sign-in."
            : action?.kind === "others"
              ? "Your current session stays signed in. Every other browser, CLI and tray session ends."
              : action?.kind === "revoke" && action.session.current
                ? "This browser will sign out. You will need to sign in again."
                : "This device will lose access and cannot renew its credentials."}</DialogDescription
        ></DialogHeader
      >
      <div class="slab-body--padded">
        {#if action?.kind === "rename"}<label
            for="security-session-name"
            class="block text-sm font-medium mb-2">Session name</label
          ><Input
            id="security-session-name"
            bind:value={label}
            maxlength={200}
            required
            disabled={busy}
          />{:else if action?.kind === "revoke"}<p class="text-sm font-medium">
            {action.session.label || "Unnamed session"}
          </p>{/if}{#if actionError}<p role="alert" class="mt-3 text-sm text-destructive">
            {actionError}
          </p>{/if}
      </div>
      <DialogFooter class="slab-actions slab-actions--row gap-0"
        ><Button type="button" variant="ghost" disabled={busy} onclick={cancelAction}>Cancel</Button
        ><Button
          type="submit"
          variant="ghost"
          class={action?.kind === "rename" ? "" : "text-destructive"}
          disabled={busy || (action?.kind === "rename" && !label.trim())}
          >{busy
            ? "Saving..."
            : action?.kind === "rename"
              ? "Save name"
              : "Confirm sign out"}</Button
        ></DialogFooter
      >
    </form>
  </DialogContent>
</Dialog>
