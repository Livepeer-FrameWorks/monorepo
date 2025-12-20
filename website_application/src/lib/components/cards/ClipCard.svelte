<script lang="ts">
	import { Badge } from "$lib/components/ui/badge";
	import { Button } from "$lib/components/ui/button";
	import { getIconComponent } from "$lib/iconUtils";
	import { getContentDeliveryUrls } from "$lib/config";
	import { toast } from "$lib/stores/toast";

	// Clip type matching Houdini schema
	interface ClipData {
		id: string;
		clipHash?: string | null;
		title?: string | null;
		description?: string | null;
		startTime?: number | null;
		endTime?: number | null;
		duration?: number | null;
		status?: string | null;
		playbackId?: string | null;
		createdAt?: string | null;
		isFrozen?: boolean;
	}

	let {
		clip,
		streamName,
		onPlay,
		class: className,
		...restProps
	}: {
		clip: ClipData;
		streamName: string;
		onPlay?: () => void; // This onPlay is for direct playback
		class?: string;
		[key: string]: unknown;
	} = $props();

	// Get delivery URLs for clip using clipHash
	let clipUrls = $derived(clip.clipHash ? getContentDeliveryUrls(clip.clipHash, "clip") : null);

	let copiedField = $state<string | null>(null);

	async function copyToClipboard(text: string, field: string, event: Event) {
		event.stopPropagation();
		try {
			await navigator.clipboard.writeText(text);
			copiedField = field;
			toast.success("Copied to clipboard");
			setTimeout(() => {
				if (copiedField === field) copiedField = null;
			}, 2000);
		} catch {
			toast.error("Failed to copy");
		}
	}

	function formatDuration(seconds: number | null | undefined) {
		if (seconds == null) return "0:00";
		const minutes = Math.floor(seconds / 60);
		const remainingSeconds = seconds % 60;
		return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
	}

	function formatDate(dateString: string | Date) {
		return new Date(dateString).toLocaleDateString();
	}

	const statusTone = $derived(
		clip.isFrozen 
			? "blue"
			: clip.status === "Available" || clip.status === "completed"
				? "green"
				: clip.status === "Processing" || clip.status === "processing"
					? "yellow"
					: clip.status === "Failed" || clip.status === "failed"
						? "red"
						: "blue"
	);

	const PlayIcon = getIconComponent("Play");
	const CopyIcon = getIconComponent("Copy");
	const CheckCircleIcon = getIconComponent("CheckCircle");
	const DownloadIcon = getIconComponent("Download");
</script>

<div
	class="slab h-full !p-0 transition-all group hover:bg-muted/30"
	{...restProps}
>
	<div class="slab-header flex items-center justify-between gap-2">
		<div class="min-w-0">
			<h3 class="font-semibold text-foreground truncate">{clip.title}</h3>
			<p class="text-xs text-muted-foreground">From: {streamName}</p>
		</div>
		<Badge tone={statusTone} class="text-xs shrink-0">
			{clip.isFrozen ? "Frozen" : clip.status}
		</Badge>
	</div>

	<div class="slab-body--padded flex-1 flex flex-col justify-between">
		{#if clip.description}
			<p class="text-sm text-muted-foreground mb-3 line-clamp-2" title={clip.description}>
				{clip.description}
			</p>
		{/if}

		<div class="space-y-2 text-sm mt-auto">
			<div class="flex justify-between">
				<span class="text-muted-foreground">Duration</span>
				<span class="text-foreground">{formatDuration(clip.duration)}</span>
			</div>
			<div class="flex justify-between">
				<span class="text-muted-foreground">Start Time</span>
				<span class="text-foreground">{formatDuration(clip.startTime)}</span>
			</div>
			<div class="flex justify-between">
				<span class="text-muted-foreground">Created</span>
				<span class="text-foreground">{clip.createdAt ? formatDate(clip.createdAt) : "N/A"}</span>
			</div>
		</div>
	</div>

	{#if (clip.status === "Available" || clip.status === "completed")}
		<div class="slab-actions flex divide-x divide-border/30">
			{#if onPlay}
				<button
					class="flex-1 flex items-center justify-center py-3 text-sm font-medium text-primary hover:bg-primary/5 transition-colors"
					onclick={(event) => {
						event.stopPropagation();
						onPlay();
					}}
				>
					{#if PlayIcon}
						<PlayIcon class="size-4 mr-1.5" />
					{/if}
					<span>Play</span>
				</button>
			{/if}
			{#if clipUrls?.primary.hls}
				<button
					class="flex-1 flex items-center justify-center py-3 text-sm font-medium text-muted-foreground hover:text-foreground hover:bg-muted/10 transition-colors"
					onclick={(event) => copyToClipboard(clipUrls.primary.hls, `hls-${clip.id}`, event)}
					title="Copy HLS URL"
				>
					{#if copiedField === `hls-${clip.id}`}
						<CheckCircleIcon class="size-4 mr-1.5" />
					{:else}
						<CopyIcon class="size-4 mr-1.5" />
					{/if}
					<span>HLS</span>
				</button>
				<a
					href={clipUrls.primary.mp4}
					target="_blank"
					rel="noopener noreferrer"
					class="flex-1 flex items-center justify-center py-3 text-sm font-medium text-muted-foreground hover:text-foreground hover:bg-muted/10 transition-colors"
					onclick={(event) => event.stopPropagation()}
					title="Download MP4"
				>
					<DownloadIcon class="size-4 mr-1.5" />
					<span>MP4</span>
				</a>
			{/if}
		</div>
	{/if}
</div>

<style>
	.line-clamp-2 {
		display: -webkit-box;
		-webkit-line-clamp: 2;
		-webkit-box-orient: vertical;
		line-clamp: 2;
		overflow: hidden;
	}
</style>
