<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { connect, getConnectors } from 'wagmi/actions';
	import { wagmiConfig } from '$lib/wallet/config';
	import {
		setupWalletWatcher,
		cleanupWalletWatcher,
		signAuthMessage,
		disconnectWallet
	} from '$lib/wallet/store.svelte';
	import { auth } from '$lib/stores/auth';
	import { Button } from '$lib/components/ui/button';

	interface Props {
		mode?: 'login' | 'link';
		onSuccess?: () => void;
		onError?: (error: string) => void;
	}

	let { mode = 'login', onSuccess, onError }: Props = $props();

	let loading = $state(false);
	let error = $state('');
	let connectors = $state<ReturnType<typeof getConnectors>>([]);

	onMount(() => {
		setupWalletWatcher();
		connectors = getConnectors(wagmiConfig);
	});

	onDestroy(() => {
		cleanupWalletWatcher();
	});

	async function handleConnect(connector: (typeof connectors)[0]) {
		loading = true;
		error = '';

		try {
			// Connect wallet
			const result = await connect(wagmiConfig, { connector });

			if (!result.accounts[0]) {
				throw new Error('No account connected');
			}

			const address = result.accounts[0];

			// Sign authentication message
			const signed = await signAuthMessage();
			if (!signed) {
				throw new Error('Failed to sign message');
			}

			// Use auth store's walletLogin (sets httpOnly cookies)
			const loginResult = await auth.walletLogin(address, signed.message, signed.signature);

			if (loginResult.success) {
				onSuccess?.();
				if (mode === 'login') {
					goto(resolve('/'));
				}
			} else {
				throw new Error(loginResult.error || 'Wallet login failed');
			}
		} catch (err) {
			const message = err instanceof Error ? err.message : 'Wallet connection failed';
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
		<div class="px-4 py-2 bg-destructive/10 border-b border-destructive/30 text-destructive text-sm">
			{error}
		</div>
	{/if}

	<div class="px-4 py-3">
		<p class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-3">
			{mode === 'login' ? 'Or sign in with wallet' : 'Connect a wallet'}
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
						<svg class="w-5 h-5 text-muted-foreground" fill="none" stroke="currentColor" viewBox="0 0 24 24">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 10h18M7 15h1m4 0h1m-7 4h12a3 3 0 003-3V8a3 3 0 00-3-3H6a3 3 0 00-3 3v8a3 3 0 003 3z" />
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
