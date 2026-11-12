<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { connect, getConnectors } from "wagmi/actions";
  import { wagmiConfig } from "$lib/wallet/config";
  import {
    setupWalletWatcher,
    cleanupWalletWatcher,
    signAuthMessage,
    disconnectWallet,
  } from "$lib/wallet/store.svelte";
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
  import {
    loadSecurityIdentity,
    changeSecurityPassword,
    linkSecurityEmail,
    linkSecurityWallet,
    unlinkSecurityWallet,
    securityTimestamp,
    type SecurityIdentity,
    type LinkedWallet,
  } from "$lib/account-security";

  let { onSecurityChange }: { onSecurityChange: () => void } = $props();
  let identity = $state<SecurityIdentity | null>(null);
  let loading = $state(true);
  let loadError = $state("");
  let passwordError = $state("");
  let walletError = $state("");
  let passwordBusy = $state(false);
  let walletBusy = $state(false);
  let currentPassword = $state("");
  let newPassword = $state("");
  let confirmation = $state("");
  let email = $state("");
  let selectedWallet = $state<LinkedWallet | null>(null);
  let connectors = $state<ReturnType<typeof getConnectors>>([]);
  let loadRequest = 0;
  const finalWalletBlocked = $derived(identity?.wallets.length === 1 && !identity.emailVerified);
  function message(error: unknown) {
    return error instanceof Error ? error.message : "The security request failed. Try again.";
  }
  async function readIdentity() {
    const request = ++loadRequest;
    loading = true;
    loadError = "";
    try {
      const result = await loadSecurityIdentity();
      if (request === loadRequest) identity = result;
    } catch (error) {
      if (request === loadRequest) loadError = message(error);
    } finally {
      if (request === loadRequest) loading = false;
    }
  }
  onMount(() => {
    void readIdentity();
    setupWalletWatcher();
    connectors = getConnectors(wagmiConfig);
  });
  onDestroy(() => {
    loadRequest++;
    cleanupWalletWatcher();
  });
  async function savePassword() {
    if (!identity || passwordBusy) return;
    passwordError = "";
    if (newPassword.length < 8) {
      passwordError = "Use a password with at least 8 characters.";
      return;
    }
    if (newPassword !== confirmation) {
      passwordError = "Passwords do not match.";
      return;
    }
    passwordBusy = true;
    try {
      if (identity.email) {
        const count = await changeSecurityPassword(currentPassword, newPassword);
        toast.success(
          `Password changed. Signed out ${count} other session${count === 1 ? "" : "s"}.`
        );
      } else {
        await linkSecurityEmail(email.trim(), newPassword);
        toast.success(
          "Email linked. Check your inbox to verify it. Other sessions were signed out."
        );
      }
      currentPassword = "";
      newPassword = "";
      confirmation = "";
      email = "";
      onSecurityChange();
      await readIdentity();
      void auth.checkAuth(true);
    } catch (error) {
      passwordError = message(error);
    } finally {
      passwordBusy = false;
    }
  }
  async function linkWallet(connector: (typeof connectors)[number]) {
    if (walletBusy) return;
    walletBusy = true;
    walletError = "";
    try {
      const result = await connect(wagmiConfig, { connector });
      const address = result.accounts[0];
      if (!address) throw new Error("No wallet account connected.");
      const challenge = await auth.issueWalletChallenge(address, result.chainId);
      const signed = await signAuthMessage(challenge);
      if (!signed) throw new Error("The wallet message was not signed.");
      await linkSecurityWallet({ address, message: signed.message, signature: signed.signature });
      toast.success("Wallet linked");
      await readIdentity();
      onSecurityChange();
    } catch (error) {
      walletError = message(error);
      await disconnectWallet();
    } finally {
      walletBusy = false;
    }
  }
  function cancelUnlink() {
    if (!walletBusy) {
      selectedWallet = null;
      walletError = "";
    }
  }
  async function confirmUnlink() {
    if (!selectedWallet || walletBusy || finalWalletBlocked) return;
    walletBusy = true;
    walletError = "";
    try {
      await unlinkSecurityWallet(selectedWallet.id);
      selectedWallet = null;
      toast.success("Wallet unlinked");
      await readIdentity();
      onSecurityChange();
    } catch (error) {
      walletError = message(error);
    } finally {
      walletBusy = false;
    }
  }
</script>

<div class="slab">
  <div class="slab-header"><h2>Password</h2></div>
  {#if loadError}<div class="slab-body--padded">
      <p role="alert" class="text-sm text-destructive">
        Could not load sign-in methods: {loadError}
      </p>
      <Button variant="outline" class="mt-3" disabled={loading} onclick={readIdentity}
        >Retry sign-in methods</Button
      >
    </div>
  {:else if loading && !identity}<p class="slab-body--padded text-muted-foreground" role="status">
      Loading password settings...
    </p>{/if}
  {#if identity}
    <form
      onsubmit={(event) => {
        event.preventDefault();
        void savePassword();
      }}
    >
      <div class="slab-body--padded space-y-4 max-w-xl">
        <p class="text-sm text-muted-foreground">
          {identity.email
            ? "Changing your password ends other sessions and keeps this one."
            : "Add and verify an email address to enable password sign-in. Linking it ends other sessions and keeps this one."}
        </p>
        {#if identity.email}
          <p class="text-sm">{identity.email}</p>
          {#if !identity.emailVerified}<p class="text-sm text-warning">
              Verify your email before using password sign-in. Check your inbox for the verification
              link.
            </p>{/if}
          <div>
            <label for="security-current-password" class="block text-sm font-medium mb-2"
              >Current password</label
            ><Input
              id="security-current-password"
              type="password"
              autocomplete="current-password"
              bind:value={currentPassword}
              required
              disabled={passwordBusy}
            />
          </div>
        {:else}
          <div>
            <label for="security-link-email" class="block text-sm font-medium mb-2"
              >Email address</label
            ><Input
              id="security-link-email"
              type="email"
              autocomplete="email"
              bind:value={email}
              required
              disabled={passwordBusy}
            />
          </div>
        {/if}
        <div>
          <label for="security-new-password" class="block text-sm font-medium mb-2"
            >New password</label
          ><Input
            id="security-new-password"
            type="password"
            autocomplete="new-password"
            bind:value={newPassword}
            minlength={8}
            required
            disabled={passwordBusy}
          />
        </div>
        <div>
          <label for="security-confirm-password" class="block text-sm font-medium mb-2"
            >Confirm new password</label
          ><Input
            id="security-confirm-password"
            type="password"
            autocomplete="new-password"
            bind:value={confirmation}
            minlength={8}
            required
            disabled={passwordBusy}
          />
        </div>
        {#if passwordError}<p role="alert" class="text-sm text-destructive">{passwordError}</p>{/if}
      </div>
      <div class="slab-actions">
        <Button type="submit" variant="ghost" disabled={passwordBusy || loading}
          >{passwordBusy
            ? "Saving..."
            : identity.email
              ? "Change password"
              : "Link email and password"}</Button
        >
      </div>
    </form>
  {/if}
</div>
<div class="slab">
  <div class="slab-header"><h2>Sign-in methods</h2></div>
  <div class="slab-body--padded">
    {#if identity}
      {#if identity.wallets.length === 0}<p class="text-sm text-muted-foreground">
          No wallets linked.
        </p>{/if}
      <ul class="divide-y divide-border">
        {#each identity.wallets as wallet (wallet.id)}<li
            class="py-3 flex items-start justify-between gap-4"
          >
            <div class="min-w-0">
              <p class="font-mono text-sm break-all">{wallet.address}</p>
              <p class="mt-1 text-xs text-muted-foreground">
                Linked {securityTimestamp(wallet.createdAt)}
              </p>
              {#if wallet.lastAuthAt}<p class="mt-1 text-xs text-muted-foreground">
                  Last sign-in {securityTimestamp(wallet.lastAuthAt)}
                </p>{/if}
            </div>
            <Button
              variant="outline"
              size="sm"
              aria-label={`Unlink wallet ${wallet.address}`}
              disabled={walletBusy || finalWalletBlocked || loading}
              onclick={() => {
                selectedWallet = wallet;
                walletError = "";
              }}>Unlink</Button
            >
          </li>{/each}
      </ul>
      {#if finalWalletBlocked}<p class="mt-3 text-sm text-muted-foreground">
          Verify an email and password sign-in before removing your final wallet.
        </p>{/if}
    {/if}
    {#if walletError && !selectedWallet}<p role="alert" class="mt-3 text-sm text-destructive">
        {walletError}
      </p>{/if}
    <p class="mt-4 text-sm text-muted-foreground">
      Connect and sign a wallet message to add another sign-in method.
    </p>
    {#if connectors.length > 0}<div class="mt-3 flex flex-wrap gap-2">
        {#each connectors as connector (connector.id)}<Button
            variant="outline"
            disabled={walletBusy || loading || !identity}
            onclick={() => linkWallet(connector)}
            >{walletBusy ? "Connecting..." : `Link ${connector.name}`}</Button
          >{/each}
      </div>{:else}<p class="mt-3 text-sm text-muted-foreground">
        No wallet extensions detected.
      </p>{/if}
  </div>
</div>
<Dialog
  open={selectedWallet !== null}
  onOpenChange={(open) => {
    if (!open) cancelUnlink();
  }}
>
  <DialogContent
    class="max-w-md rounded-none border-border bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left"
      ><DialogTitle>Unlink wallet</DialogTitle><DialogDescription
        >Remove this wallet as a sign-in method.</DialogDescription
      ></DialogHeader
    >
    <div class="slab-body--padded">
      <p class="font-mono text-sm break-all">{selectedWallet?.address}</p>
      {#if walletError}<p role="alert" class="mt-3 text-sm text-destructive">{walletError}</p>{/if}
    </div>
    <DialogFooter class="slab-actions slab-actions--row gap-0"
      ><Button variant="ghost" disabled={walletBusy} onclick={cancelUnlink}>Cancel</Button><Button
        variant="ghost"
        class="text-destructive"
        disabled={walletBusy || finalWalletBlocked}
        onclick={confirmUnlink}>{walletBusy ? "Unlinking..." : "Confirm unlink"}</Button
      ></DialogFooter
    >
  </DialogContent>
</Dialog>
