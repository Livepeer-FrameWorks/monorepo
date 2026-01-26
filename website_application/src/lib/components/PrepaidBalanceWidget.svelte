<script lang="ts">
	import { resolve } from '$app/paths';
	import { getIconComponent } from '$lib/iconUtils';
	import { Button } from '$lib/components/ui/button';
	import { GetPrepaidBalanceStore } from '$houdini';

	const WalletIcon = getIconComponent('Wallet');
	const AlertIcon = getIconComponent('AlertTriangle');
	const ArrowRightIcon = getIconComponent('ArrowRight');

	const balanceQuery = new GetPrepaidBalanceStore();

	let loading = $state(true);
	let balanceCents = $state(0);
	let currency = $state('USD');
	let isLowBalance = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		loadBalance();
	});

	async function loadBalance() {
		loading = true;
		error = null;
		try {
			const result = await balanceQuery.fetch();
			if (result.data?.prepaidBalance) {
				balanceCents = result.data.prepaidBalance.balanceCents;
				currency = result.data.prepaidBalance.currency;
				isLowBalance = result.data.prepaidBalance.isLowBalance;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load balance';
		} finally {
			loading = false;
		}
	}

	function formatCurrency(cents: number, curr: string): string {
		const amount = cents / 100;
		return new Intl.NumberFormat('en-US', {
			style: 'currency',
			currency: curr
		}).format(amount);
	}

	const balanceDisplay = $derived(formatCurrency(balanceCents, currency));
	const isNegative = $derived(balanceCents < 0);
</script>

<div class="slab">
	<div class="slab-header">
		<div class="flex items-center gap-2">
			<WalletIcon class="w-4 h-4 text-primary" />
			<h3>Prepaid Balance</h3>
		</div>
	</div>

	<div class="slab-body--padded">
		{#if loading}
			<div class="flex items-center justify-center py-4">
				<div class="loading-spinner w-6 h-6"></div>
			</div>
		{:else if error}
			<div class="text-destructive text-sm">{error}</div>
		{:else}
			<div class="space-y-3">
				<div class="flex items-baseline gap-2">
					<span
						class="text-3xl font-bold tabular-nums"
						class:text-destructive={isNegative}
						class:text-foreground={!isNegative}
					>
						{balanceDisplay}
					</span>
					<span class="text-sm text-muted-foreground">{currency}</span>
				</div>

				{#if isLowBalance}
					<div class="flex items-center gap-2 p-2 bg-warning/10 rounded-md border border-warning/30">
						<AlertIcon class="w-4 h-4 text-warning shrink-0" />
						<span class="text-sm text-warning">
							Low balance - top up to continue using services
						</span>
					</div>
				{/if}

				{#if isNegative}
					<div class="flex items-center gap-2 p-2 bg-destructive/10 rounded-md border border-destructive/30">
						<AlertIcon class="w-4 h-4 text-destructive shrink-0" />
						<span class="text-sm text-destructive">
							Negative balance - new streams blocked until topped up
						</span>
					</div>
				{/if}
			</div>
		{/if}
	</div>

	<div class="slab-actions">
		<Button href={resolve('/account/balance')} variant="ghost" class="gap-2">
			Top Up Balance
			<ArrowRightIcon class="w-4 h-4" />
		</Button>
	</div>
</div>
