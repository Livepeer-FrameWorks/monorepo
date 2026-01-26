<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { connect, getConnectors } from 'wagmi/actions';
	import { wagmiConfig, getChainType } from '$lib/wallet/config';
	import {
		setupWalletWatcher,
		cleanupWalletWatcher,
		signAuthMessage,
		disconnectWallet,
		getWalletState
	} from '$lib/wallet/store';
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

	const walletState = getWalletState();

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

	function getConnectorIcon(connectorName: string): string {
		const name = connectorName.toLowerCase();
		if (name.includes('metamask')) return '/icons/metamask.svg';
		if (name.includes('coinbase')) return '/icons/coinbase.svg';
		if (name.includes('walletconnect')) return '/icons/walletconnect.svg';
		return '/icons/wallet.svg';
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
			{#each connectors as connector}
				<Button
					type="button"
					variant="outline"
					class="w-full justify-start gap-3"
					disabled={loading}
					onclick={() => handleConnect(connector)}
				>
					{#if loading}
						<div class="loading-spinner w-5 h-5"></div>
					{:else}
						<img src={getConnectorIcon(connector.name)} alt="" class="w-5 h-5" onerror={(e) => { (e.currentTarget as HTMLImageElement).style.display = 'none'; }} />
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
