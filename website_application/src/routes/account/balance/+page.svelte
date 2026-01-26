<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { getIconComponent } from '$lib/iconUtils';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { toast } from '$lib/stores/toast.js';
	import { getDocsSiteUrl } from '$lib/config';
	import {
		GetPrepaidBalanceStore,
		GetBalanceTransactionsStore,
		CreateCardTopupStore,
		CreateCryptoTopupStore
	} from '$houdini';

	// Icons
	const WalletIcon = getIconComponent('Wallet');
	const CreditCardIcon = getIconComponent('CreditCard');
	const CoinsIcon = getIconComponent('Coins');
	const HistoryIcon = getIconComponent('History');
	const RefreshIcon = getIconComponent('RefreshCw');
	const AlertIcon = getIconComponent('AlertTriangle');
	const CheckIcon = getIconComponent('Check');
	const CopyIcon = getIconComponent('Copy');

	// Stores
	const balanceQuery = new GetPrepaidBalanceStore();
	const transactionsQuery = new GetBalanceTransactionsStore();
	const cardTopupMutation = new CreateCardTopupStore();
	const cryptoTopupMutation = new CreateCryptoTopupStore();
	const docsSiteUrl = getDocsSiteUrl().replace(/\/$/, "");

	// State
	let loading = $state(true);
	let balance = $state<{
		balanceCents: number;
		currency: string;
		isLowBalance: boolean;
	} | null>(null);
	let transactions = $state<Array<{
		id: string;
		amountCents: number;
		balanceAfterCents: number;
		transactionType: string;
		description: string | null;
		createdAt: string;
	}>>([]);
	let totalTransactions = $state(0);

	// Top-up form state
	let topupAmount = $state(10); // Default $10
	let topupMethod = $state<'card' | 'crypto'>('card');
	let cryptoAsset = $state<'ETH' | 'USDC' | 'LPT'>('USDC');
	let topupLoading = $state(false);

	// Crypto deposit state
	let cryptoDeposit = $state<{
		topupId: string;
		depositAddress: string;
		asset: string;
		expiresAt: string;
	} | null>(null);

	onMount(() => {
		loadData();
	});

	async function loadData() {
		loading = true;
		try {
			const [balanceResult, txResult] = await Promise.all([
				balanceQuery.fetch(),
				transactionsQuery.fetch({ variables: { page: { first: 10 } } })
			]);

			if (balanceResult.data?.prepaidBalance) {
				balance = {
					balanceCents: balanceResult.data.prepaidBalance.balanceCents,
					currency: balanceResult.data.prepaidBalance.currency,
					isLowBalance: balanceResult.data.prepaidBalance.isLowBalance
				};
			}

			if (txResult.data?.balanceTransactionsConnection) {
				transactions = txResult.data.balanceTransactionsConnection.nodes.map((n) => ({
					id: n.id,
					amountCents: n.amountCents,
					balanceAfterCents: n.balanceAfterCents,
					transactionType: n.transactionType,
					description: n.description,
					createdAt: n.createdAt
				}));
				totalTransactions = txResult.data.balanceTransactionsConnection.totalCount;
			}
		} catch {
			toast.error('Failed to load balance data');
		} finally {
			loading = false;
		}
	}

	async function handleCardTopup() {
		if (topupAmount <= 0) {
			toast.error('Please enter an amount');
			return;
		}

		topupLoading = true;
		try {
			const result = await cardTopupMutation.mutate({
				input: {
					amountCents: topupAmount * 100,
					currency: 'EUR',
					provider: 'STRIPE',
					successUrl: `${window.location.origin}${resolve('/account/balance')}?success=true`,
					cancelUrl: `${window.location.origin}${resolve('/account/balance')}?cancelled=true`
				}
			});

			if (result.data?.createCardTopup?.checkoutUrl) {
				window.location.href = result.data.createCardTopup.checkoutUrl;
			}
		} catch {
			toast.error('Failed to create checkout session');
		} finally {
			topupLoading = false;
		}
	}

	async function handleCryptoTopup() {
		if (topupAmount <= 0) {
			toast.error('Please enter an amount');
			return;
		}

		topupLoading = true;
		try {
			const result = await cryptoTopupMutation.mutate({
				input: {
					amountCents: topupAmount * 100,
					asset: cryptoAsset,
					currency: 'EUR'
				}
			});

			if (result.data?.createCryptoTopup) {
				cryptoDeposit = {
					topupId: result.data.createCryptoTopup.topupId,
					depositAddress: result.data.createCryptoTopup.depositAddress,
					asset: result.data.createCryptoTopup.assetSymbol,
					expiresAt: result.data.createCryptoTopup.expiresAt
				};
				toast.success('Deposit address created');
			}
		} catch {
			toast.error('Failed to create deposit address');
		} finally {
			topupLoading = false;
		}
	}

	function copyAddress() {
		if (cryptoDeposit?.depositAddress) {
			navigator.clipboard.writeText(cryptoDeposit.depositAddress);
			toast.success('Address copied to clipboard');
		}
	}

	function formatCurrency(cents: number, curr: string = 'EUR'): string {
		const amount = cents / 100;
		return new Intl.NumberFormat('en-IE', {
			style: 'currency',
			currency: curr
		}).format(amount);
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	function getTransactionIcon(type: string) {
		switch (type) {
			case 'topup':
				return CheckIcon;
			case 'usage':
				return HistoryIcon;
			default:
				return CoinsIcon;
		}
	}

	function isPositiveAmount(cents: number): boolean {
		return cents >= 1;
	}
</script>

<svelte:head>
	<title>Balance - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
	<!-- Fixed Page Header -->
	<div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
		<div class="flex justify-between items-center">
			<div class="flex items-center gap-3">
				<WalletIcon class="w-5 h-5 text-primary" />
				<div>
					<h1 class="text-xl font-bold text-foreground">Prepaid Balance</h1>
					<p class="text-sm text-muted-foreground">Manage your prepaid balance and top-ups</p>
				</div>
			</div>
			<Button variant="ghost" onclick={loadData} disabled={loading}>
				<RefreshIcon class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
			</Button>
		</div>
	</div>

	<!-- Scrollable Content -->
	<div class="flex-1 overflow-y-auto">
		{#if loading}
			<div class="flex items-center justify-center min-h-64">
				<div class="loading-spinner w-8 h-8"></div>
			</div>
		{:else}
			<div class="dashboard-grid">
				<!-- Current Balance Card -->
				<div class="slab">
					<div class="slab-header">
						<div class="flex items-center gap-2">
							<WalletIcon class="w-4 h-4 text-primary" />
							<h3>Current Balance</h3>
						</div>
					</div>
					<div class="slab-body--padded">
						{#if balance}
							<div class="text-4xl font-bold tabular-nums" class:text-destructive={balance.balanceCents < 0}>
								{formatCurrency(balance.balanceCents, balance.currency)}
							</div>
							{#if balance.isLowBalance}
								<div class="flex items-center gap-2 mt-3 text-warning">
									<AlertIcon class="w-4 h-4" />
									<span class="text-sm">Low balance warning</span>
								</div>
							{/if}
							<p class="text-sm text-muted-foreground mt-4">
								Balance is used for streaming, storage, and transcoding. API requests are free.
								<a href={resolve(`${docsSiteUrl}/streamers/billing`)} class="text-primary hover:underline" target="_blank" rel="noopener">Learn more</a>
							</p>
						{:else}
							<p class="text-muted-foreground">No balance data available</p>
						{/if}
					</div>
				</div>

				<!-- Top-Up Card -->
				<div class="slab">
					<div class="slab-header">
						<div class="flex items-center gap-2">
							<CoinsIcon class="w-4 h-4 text-success" />
							<h3>Top Up Balance</h3>
						</div>
					</div>
					<div class="slab-body--padded space-y-4">
						<!-- Amount -->
						<div>
							<label class="block text-sm font-medium text-muted-foreground mb-2">Amount (EUR)</label>
							<div class="flex gap-2">
								{#each [5, 10, 25, 50, 100] as amount (amount)}
									<Button
										variant={topupAmount === amount ? 'default' : 'outline'}
										size="sm"
										onclick={() => (topupAmount = amount)}
									>
										${amount}
									</Button>
								{/each}
							</div>
							<div class="mt-2">
								<Input
									type="number"
									min={1}
									step={1}
									bind:value={topupAmount}
									placeholder="Custom amount"
									class="w-32"
								/>
							</div>
						</div>

						<!-- Method -->
						<div>
							<label class="block text-sm font-medium text-muted-foreground mb-2">Payment Method</label>
							<div class="flex gap-2">
								<Button
									variant={topupMethod === 'card' ? 'default' : 'outline'}
									onclick={() => (topupMethod = 'card')}
									class="gap-2"
								>
									<CreditCardIcon class="w-4 h-4" />
									Card
								</Button>
								<Button
									variant={topupMethod === 'crypto' ? 'default' : 'outline'}
									onclick={() => (topupMethod = 'crypto')}
									class="gap-2"
								>
									<CoinsIcon class="w-4 h-4" />
									Crypto
								</Button>
							</div>
						</div>

						{#if topupMethod === 'crypto'}
							<div>
								<label class="block text-sm font-medium text-muted-foreground mb-2">Asset</label>
								<div class="flex gap-2">
									{#each ['ETH', 'USDC', 'LPT'] as asset (asset)}
										<Button
											variant={cryptoAsset === asset ? 'default' : 'outline'}
											size="sm"
											onclick={() => (cryptoAsset = asset as 'ETH' | 'USDC' | 'LPT')}
										>
											{asset}
										</Button>
									{/each}
								</div>
							</div>
						{/if}
					</div>
					<div class="slab-actions">
						{#if topupMethod === 'card'}
							<Button onclick={handleCardTopup} disabled={topupLoading || topupAmount <= 0} class="gap-2">
								{topupLoading ? 'Processing...' : `Pay ${formatCurrency(topupAmount * 100)}`}
							</Button>
						{:else}
							<Button onclick={handleCryptoTopup} disabled={topupLoading || topupAmount <= 0} class="gap-2">
								{topupLoading ? 'Creating...' : 'Get Deposit Address'}
							</Button>
						{/if}
					</div>
				</div>

				<!-- Crypto Deposit Address (if created) -->
				{#if cryptoDeposit}
					<div class="slab col-span-full">
						<div class="slab-header">
							<div class="flex items-center gap-2">
								<CoinsIcon class="w-4 h-4 text-success" />
								<h3>Deposit {cryptoDeposit.asset}</h3>
							</div>
						</div>
						<div class="slab-body--padded">
							<p class="text-sm text-muted-foreground mb-3">
								Send {cryptoDeposit.asset} to this address. Your balance will be credited automatically after confirmation.
							</p>
							<div class="flex items-center gap-2 p-3 bg-muted/30 rounded-md font-mono text-sm break-all">
								<span class="flex-1">{cryptoDeposit.depositAddress}</span>
								<Button variant="ghost" size="sm" onclick={copyAddress}>
									<CopyIcon class="w-4 h-4" />
								</Button>
							</div>
							<p class="text-xs text-muted-foreground mt-2">
								Expires: {formatDate(cryptoDeposit.expiresAt)}
							</p>
						</div>
					</div>
				{/if}

				<!-- Transaction History -->
				<div class="slab col-span-full">
					<div class="slab-header">
						<div class="flex items-center gap-2">
							<HistoryIcon class="w-4 h-4 text-muted-foreground" />
							<h3>Transaction History</h3>
						</div>
						<span class="text-sm text-muted-foreground">{totalTransactions} transactions</span>
					</div>
					<div class="slab-body">
						{#if transactions.length === 0}
							<div class="p-4 text-center text-muted-foreground">No transactions yet</div>
						{:else}
							<div class="divide-y divide-[hsl(var(--tn-fg-gutter)/0.3)]">
								{#each transactions as tx (tx.id)}
									{@const TxIcon = getTransactionIcon(tx.transactionType)}
									<div class="flex items-center justify-between p-4">
										<div class="flex items-center gap-3">
											{#if isPositiveAmount(tx.amountCents)}
												<div class="w-8 h-8 rounded-full flex items-center justify-center bg-success/10">
													<TxIcon class="w-4 h-4 text-success" />
												</div>
											{:else}
												<div class="w-8 h-8 rounded-full flex items-center justify-center bg-muted">
													<TxIcon class="w-4 h-4 text-muted-foreground" />
												</div>
											{/if}
											<div>
												<p class="font-medium capitalize">{tx.transactionType}</p>
												<p class="text-sm text-muted-foreground">
													{tx.description || formatDate(tx.createdAt)}
												</p>
											</div>
										</div>
										<div class="text-right">
											{#if isPositiveAmount(tx.amountCents)}
												<p class="font-medium tabular-nums text-success">
													+{formatCurrency(tx.amountCents)}
												</p>
											{:else}
												<p class="font-medium tabular-nums text-foreground">
													{formatCurrency(tx.amountCents)}
												</p>
											{/if}
											<p class="text-sm text-muted-foreground tabular-nums">
												Balance: {formatCurrency(tx.balanceAfterCents)}
											</p>
										</div>
									</div>
								{/each}
							</div>
						{/if}
					</div>
				</div>
			</div>
		{/if}
	</div>
</div>
