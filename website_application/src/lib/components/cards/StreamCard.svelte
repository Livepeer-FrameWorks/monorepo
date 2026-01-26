<script lang="ts">
	import { cn } from "$lib/utils";
	import { Badge } from "$lib/components/ui/badge";
	import { StreamStatus } from "$houdini";

	interface StreamCardData {
		id: string;
		streamId?: string;
		name: string;
		metrics?: {
			status?: string | null;
			isLive?: boolean | null;
			currentViewers?: number | null;
		} | null;
		viewers?: number;
		resolution?: string;
	}

	let {
		stream,
		nodeName,
		class: className,
		...restProps
	}: {
		stream: StreamCardData;
		nodeName?: string;
		class?: string;
		[key: string]: unknown;
	} = $props();

	const status = $derived(stream.metrics?.status);
	const isLive = $derived(status === StreamStatus.LIVE);
	const displayStreamId = $derived(stream.streamId || stream.id);
	const displayName = $derived(
		stream.name || `Stream ${displayStreamId.slice(0, 8)}`
	);
</script>

<div class={cn("slab slab--compact", className)} {...restProps}>
	<div class="slab-header">
		<div class="flex items-center justify-between w-full">
			<h3 class="font-semibold text-foreground">
				{displayName}
			</h3>
			<Badge tone={isLive ? "green" : "red"} class="text-xs">
				<div class="flex items-center gap-1.5">
					<div
						class={cn(
							"size-1.5 rounded-full",
							isLive
								? "bg-success animate-pulse"
								: "bg-error"
						)}
					></div>
					{isLive ? "Live" : "Offline"}
				</div>
			</Badge>
		</div>
	</div>

	<div class="slab-body--padded">
		<div class="grid grid-cols-2 gap-4 text-sm">
			<div>
				<p class="text-muted-foreground text-xs">Viewers</p>
				<p class="font-semibold text-foreground">{stream.viewers || 0}</p>
			</div>
			<div>
				<p class="text-muted-foreground text-xs">Resolution</p>
				<p class="font-semibold text-foreground">
					{stream.resolution || "Unknown"}
				</p>
			</div>
		</div>
	</div>

	<div class="px-4 py-2 border-t border-border/30 bg-muted/20">
		<p class="text-xs text-muted-foreground">
			Node: <span class="text-foreground">{nodeName || "Not configured"}</span>
		</p>
	</div>
</div>
