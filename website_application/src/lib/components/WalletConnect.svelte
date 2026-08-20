<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { connect, getConnectors, watchConnectors } from "wagmi/actions";
  import { wagmiConfig } from "$lib/wallet/config";
  import {
    setupWalletWatcher,
    cleanupWalletWatcher,
    signAuthMessageForConnection,
    disconnectWallet,
  } from "$lib/wallet/store.svelte";
  import { auth } from "$lib/stores/auth";
  import { safeReturnTo } from "$lib/auth/returnTo";
  import {
    connectAndLogin,
    subscribeToConnectors,
    walletErrorMessage,
  } from "$lib/wallet/loginFlow";
  import { Button } from "$lib/components/ui/button";

  interface Props {
    mode?: "login" | "link";
    onSuccess?: () => void;
    onError?: (error: string) => void;
  }

  let { mode = "login", onSuccess, onError }: Props = $props();

  let loading = $state(false);
  let error = $state("");
  let connectors = $state<ReturnType<typeof getConnectors>>([]);

  onMount(() => {
    setupWalletWatcher();
    return subscribeToConnectors(
      () => getConnectors(wagmiConfig),
      (onChange) => watchConnectors(wagmiConfig, { onChange }),
      (nextConnectors) => (connectors = [...nextConnectors])
    );
  });

  onDestroy(() => {
    cleanupWalletWatcher();
  });

  async function handleConnect(connector: (typeof connectors)[0]) {
    loading = true;
    error = "";

    try {
      const loginResult = await connectAndLogin(connector, {
        connect: (selected) => connect(wagmiConfig, { connector: selected }),
        issueChallenge: (address, chainId) => auth.issueWalletChallenge(address, chainId),
        signChallenge: signAuthMessageForConnection,
        // The auth store completes the first-party cookie session.
        login: (address, message, signature) => auth.walletLogin(address, message, signature),
      });

      if (loginResult.success) {
        onSuccess?.();
        if (mode === "login") {
          const loginPath = resolve("/login");
          const returnTo =
            typeof window === "undefined"
              ? null
              : safeReturnTo(
                  new URLSearchParams(window.location.search).get("return_to"),
                  loginPath
                );
          // safeReturnTo accepts only a same-origin absolute path.
          // eslint-disable-next-line svelte/no-navigation-without-resolve
          goto(returnTo ?? resolve("/"));
        }
      } else {
        throw new Error(loginResult.error || "Wallet login failed");
      }
    } catch (err) {
      const message = walletErrorMessage(err);
      error = message;
      onError?.(message);

      // Disconnect on error
      await disconnectWallet();
    } finally {
      loading = false;
    }
  }
</script>

<div class="wallet-connect">
  {#if error}
    <div
      class="px-4 py-2 bg-destructive/10 border-b border-destructive/30 text-destructive text-sm"
    >
      {error}
    </div>
  {/if}

  <div class="px-4 py-3">
    <p class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-3">
      {mode === "login" ? "Or sign in with wallet" : "Connect a wallet"}
    </p>

    <div class="flex flex-col gap-2">
      {#each connectors as connector (connector.id)}
        <Button
          type="button"
          variant="outline"
          class="w-full justify-start gap-3"
          disabled={loading}
          onclick={() => handleConnect(connector)}
        >
          {#if loading}
            <div class="loading-spinner w-5 h-5"></div>
          {:else if connector.icon}
            <img src={connector.icon} alt="" class="w-5 h-5" />
          {:else}
            <svg
              class="w-5 h-5 text-muted-foreground"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M3 10h18M7 15h1m4 0h1m-7 4h12a3 3 0 003-3V8a3 3 0 00-3-3H6a3 3 0 00-3 3v8a3 3 0 003 3z"
              />
            </svg>
          {/if}
          {connector.name}
        </Button>
      {/each}
    </div>

    {#if connectors.length === 0}
      <p class="text-sm text-muted-foreground">
        No wallet extensions detected. Install MetaMask or another wallet to continue.
      </p>
    {/if}
  </div>
</div>

<style>
  .wallet-connect {
    border-top: 1px solid hsl(var(--tn-fg-gutter) / 0.3);
  }
</style>
