<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    fragment,
    GetStreamStore,
    GetStreamKeysStore,
    GetStorageArtifactsConnectionStore,
    UpdateStreamStore,
    SetStreamRetentionOverridesStore,
    DeleteStreamStore,
    RefreshStreamKeyStore,
    CreateStreamKeyStore,
    DeleteStreamKeyStore,
    StreamEventsStore,
    GetPushTargetsStore,
    CreatePushTargetStore,
    UpdatePushTargetStore,
    DeletePushTargetStore,
    TrackListUpdatesStore,
    StreamCoreFieldsStore,
    StreamMetricsFieldsStore,
  } from "$houdini";
  import type {
    StreamEvents$result,
    TrackListUpdates$result,
    GetStorageArtifactsConnection$result,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Tabs, TabsContent, TabsList, TabsTrigger } from "$lib/components/ui/tabs";
  import {
    StreamEditModal,
    StreamDeleteModal,
    StreamCreateKeyModal,
    StreamStatusCard,
    StreamKeyCard,
    StreamPlaybackCard,
    OverviewTabPanel,
    ArtefactsTabPanel,
    PlaybackTabPanel,
    StreamSetupPanel,
    PushTargetsTabPanel,
    PushTargetCreateModal,
    PushTargetEditModal,
    PlaybackAuthTabPanel,
  } from "$lib/components/stream-details";
  import { SectionDivider } from "$lib/components/layout";
  import { resolveOperationalStreamId } from "$lib/route-ids";
  import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
    DropdownMenuLabel,
    DropdownMenuSeparator,
  } from "$lib/components/ui/dropdown-menu";
  import { MoreVertical } from "lucide-svelte";

  // Houdini stores
  const streamStore = new GetStreamStore();
  const streamKeysStore = new GetStreamKeysStore();
  const storageArtifactsStore = new GetStorageArtifactsConnectionStore();
  const updateStreamMutation = new UpdateStreamStore();
  const setStreamRetentionOverridesMutation = new SetStreamRetentionOverridesStore();
  const deleteStreamMutation = new DeleteStreamStore();
  const refreshStreamKeyMutation = new RefreshStreamKeyStore();
  const createStreamKeyMutation = new CreateStreamKeyStore();
  const deleteStreamKeyMutation = new DeleteStreamKeyStore();
  const pushTargetsStore = new GetPushTargetsStore();
  const createPushTargetMutation = new CreatePushTargetStore();
  const updatePushTargetMutation = new UpdatePushTargetStore();
  const deletePushTargetMutation = new DeletePushTargetStore();
  const streamEventsSub = new StreamEventsStore();
  const trackListSub = new TrackListUpdatesStore();

  // Fragment stores for unmasking nested data
  const streamCoreStore = new StreamCoreFieldsStore();
  const streamMetricsStore = new StreamMetricsFieldsStore();

  // Types from Houdini
  type _StreamType = NonNullable<NonNullable<typeof $streamStore.data>["stream"]>;
  type _StreamKeyType = NonNullable<
    NonNullable<
      NonNullable<NonNullable<typeof $streamKeysStore.data>["streamKeysConnection"]>["edges"]
    >[0]
  >["node"];
  type TrackInfo = NonNullable<TrackListUpdates$result["liveTrackListUpdates"]>;
  type StorageArtifactNode =
    NonNullable<GetStorageArtifactsConnection$result>["storageArtifactsConnection"]["nodes"][number];
  type StreamArtifactKind = "vod" | "chapter" | "dvr" | "clip";
  type StreamArtifact = {
    key: string;
    kind: StreamArtifactKind;
    id: string;
    hash: string;
    playbackId?: string | null;
    streamId?: string | null;
    title?: string | null;
    secondaryLabel?: string | null;
    sizeBytes?: number | null;
    status?: string | null;
    storageLocation?: string | null;
    isSynced?: boolean | null;
    hasLocalCopy?: boolean | null;
    createdAt?: string | null;
    updatedAt?: string | null;
    expiresAt?: string | null;
  };

  // page is a store; derive the param so it stays in sync with navigation
  let streamId = $derived(page.params.id as string);

  // Get masked stream data from store
  let maskedStream = $derived($streamStore.data?.stream ?? null);

  // Unmask StreamCoreFields
  let streamCoreStoreResult = $derived(
    maskedStream ? fragment(maskedStream, streamCoreStore) : null
  );
  let streamCore = $derived(streamCoreStoreResult ? $streamCoreStoreResult : null);

  // Unmask StreamMetricsFields
  let streamMetricsStoreResult = $derived(
    maskedStream?.metrics ? fragment(maskedStream.metrics, streamMetricsStore) : null
  );
  let streamMetrics = $derived(streamMetricsStoreResult ? $streamMetricsStoreResult : null);

  // Combine unmasked data into stream object
  let stream = $derived(
    streamCore
      ? {
          ...streamCore,
          recentPullSourceEvents: maskedStream?.recentPullSourceEvents ?? [],
          playbackPolicy: maskedStream?.playbackPolicy ?? null,
          metrics: streamMetrics,
        }
      : null
  );

  // Derived state from Houdini stores
  // Map to create mutable objects (Houdini returns readonly types)
  let streamKeys = $derived(
    $streamKeysStore.data?.streamKeysConnection?.edges?.map((e) => ({
      id: e.node.id,
      streamId: e.node.streamId,
      keyValue: e.node.keyValue,
      keyName: e.node.keyName ?? "",
      isActive: e.node.isActive,
      createdAt: e.node.createdAt,
      lastUsedAt: e.node.lastUsedAt ?? undefined,
    })) ?? []
  );
  let pushTargets = $derived(
    $pushTargetsStore.data?.stream?.pushTargets?.map((t) => ({
      id: t.id,
      streamId: t.streamId,
      platform: t.platform ?? null,
      name: t.name,
      targetUri: t.targetUri,
      isEnabled: t.isEnabled,
      status: t.status,
      lastError: t.lastError ?? null,
      lastPushedAt: t.lastPushedAt ?? null,
      createdAt: t.createdAt,
    })) ?? []
  );
  let storageArtifacts = $derived(
    ($storageArtifactsStore.data?.storageArtifactsConnection?.nodes ?? []).map(
      normalizeStreamArtifact
    )
  );
  let recordings = $derived(
    storageArtifacts
      .filter((asset) => asset.kind === "dvr")
      .map((asset) => ({
        id: asset.id,
        dvrHash: asset.hash,
        playbackId: asset.playbackId,
        streamId: asset.streamId,
        title: asset.title,
        status: asset.status,
        createdAt: asset.createdAt,
        updatedAt: asset.updatedAt,
        expiresAt: asset.expiresAt,
        durationSeconds: null,
        sizeBytes: asset.sizeBytes,
        storageLocation: asset.storageLocation,
        isSynced: asset.isSynced,
        hasLocalCopy: asset.hasLocalCopy,
      }))
  );
  let clips = $derived(
    storageArtifacts
      .filter((asset) => asset.kind === "clip")
      .map((asset) => ({
        id: asset.id,
        clipHash: asset.hash,
        playbackId: asset.playbackId,
        streamId: asset.streamId,
        title: asset.title,
        status: asset.status,
        createdAt: asset.createdAt,
        updatedAt: asset.updatedAt,
        expiresAt: asset.expiresAt,
        duration: null,
        sizeBytes: asset.sizeBytes,
        storageLocation: asset.storageLocation,
        isSynced: asset.isSynced,
        hasLocalCopy: asset.hasLocalCopy,
      }))
  );
  let vodArtifacts = $derived(
    storageArtifacts
      .filter((asset) => asset.kind === "vod" || asset.kind === "chapter")
      .map((asset) => ({
        id: asset.id,
        artifactHash: asset.hash,
        playbackId: asset.playbackId,
        streamId: asset.streamId,
        originType: asset.kind === "chapter" ? "dvr_chapter" : null,
        originId: null,
        title: asset.title,
        filename: asset.secondaryLabel,
        status: asset.status,
        createdAt: asset.createdAt,
        updatedAt: asset.updatedAt,
        expiresAt: asset.expiresAt,
        durationMs: null,
        sizeBytes: asset.sizeBytes,
      }))
  );

  // This is the config page: viewer counts, quality/daily analytics, and health live
  // on the dedicated /streams/[id]/analytics and /streams/[id]/health routes.

  let error = $state<string | null>(null);
  let loading = $derived(!error && ($streamStore.fetching || $streamKeysStore.fetching));
  let showEditModal = $state(false);
  let showDeleteModal = $state(false);
  let showCreateKeyModal = $state(false);
  let showCreatePushTargetModal = $state(false);
  let showEditPushTargetModal = $state(false);
  let editingPushTarget = $state<(typeof pushTargets)[number] | null>(null);
  let actionLoading = $state({
    refreshKey: false,
    deleteStream: false,
    editStream: false,
    createKey: false,
    deleteKey: null as string | null,
    createPushTarget: false,
    updatePushTarget: false,
    deletePushTarget: null as string | null,
    togglePushTarget: null as string | null,
  });

  function normalizeStreamArtifact(asset: StorageArtifactNode): StreamArtifact {
    const rawKind = asset.kind.toLowerCase() as StreamArtifactKind;
    return {
      key: asset.key,
      kind: rawKind,
      id: asset.id,
      hash: asset.hash,
      playbackId: asset.playbackId,
      streamId: asset.streamId,
      title: asset.title,
      secondaryLabel: asset.secondaryLabel,
      sizeBytes: asset.sizeBytes,
      status: asset.status,
      storageLocation: asset.storageLocation,
      isSynced: asset.isSynced,
      hasLocalCopy: asset.hasLocalCopy,
      createdAt: asset.createdAt,
      updatedAt: asset.updatedAt,
      expiresAt: asset.expiresAt,
    };
  }

  function storageKindsForStream(): ("DVR" | "CHAPTER" | "CLIP")[] {
    return ["DVR", "CHAPTER", "CLIP"];
  }

  // Auto-refresh interval for live data (fallback)
  let refreshInterval: ReturnType<typeof setInterval> | null = null;

  // Current track info from subscription
  let currentTracks = $state<TrackInfo | null>(null);

  // Derived: is stream live?
  let isLive = $derived(stream?.metrics?.isLive ?? false);

  // Fallback track info from StreamMetricsFields when subscription hasn't fired yet
  // This uses the primary track data that's already fetched with the stream query
  // Type assertion needed because we're creating a partial match for display purposes
  let fallbackTracks = $derived.by((): TrackInfo | null => {
    if (!streamMetrics?.isLive || !streamMetrics?.primaryCodec) return null;
    // Create a fallback that satisfies the TrackInfo type from the subscription
    // OverviewTabPanel only uses a subset of these fields for display
    return {
      streamId:
        resolveOperationalStreamId({ routeParamId: streamId, streamUuid: stream?.streamId }) ?? "",
      totalTracks: 1,
      videoTrackCount: 1,
      audioTrackCount: 0,
      qualityTier: streamMetrics.qualityTier ?? null,
      primaryWidth: streamMetrics.primaryWidth ?? null,
      primaryHeight: streamMetrics.primaryHeight ?? null,
      primaryFps: streamMetrics.primaryFps ?? null,
      primaryVideoBitrate: streamMetrics.primaryBitrate ?? null,
      primaryVideoCodec: streamMetrics.primaryCodec ?? null,
      tracks: [
        {
          trackName: "video0",
          trackType: "video",
          codec: streamMetrics.primaryCodec ?? null,
          width: streamMetrics.primaryWidth ?? null,
          height: streamMetrics.primaryHeight ?? null,
          fps: streamMetrics.primaryFps ?? null,
          bitrateKbps: streamMetrics.primaryBitrate
            ? Math.round(streamMetrics.primaryBitrate / 1000)
            : null,
          bitrateBps: streamMetrics.primaryBitrate ?? null,
          buffer: null,
          jitter: null,
          resolution:
            streamMetrics.primaryWidth && streamMetrics.primaryHeight
              ? `${streamMetrics.primaryWidth}x${streamMetrics.primaryHeight}`
              : null,
          hasBFrames: null,
          channels: null,
          sampleRate: null,
        },
      ],
    };
  });

  // Effect to handle subscription errors
  $effect(() => {
    if ($streamEventsSub.errors?.length) {
      console.warn("Stream events subscription error:", $streamEventsSub.errors);
    }
    if ($trackListSub.errors?.length) {
      console.warn("Track list subscription error:", $trackListSub.errors);
    }
  });

  // Effect to handle stream events subscription
  // Use untrack to prevent effect loops when mutating state
  $effect(() => {
    const event = $streamEventsSub.data?.liveStreamEvents;
    if (event) {
      untrack(() => handleStreamEvent(event));
    }
  });

  // Effect to handle track list subscription
  $effect(() => {
    const tracks = $trackListSub.data?.liveTrackListUpdates;
    if (tracks) {
      untrack(() => {
        currentTracks = tracks;
      });
    }
  });

  onMount(async () => {
    await loadStreamData();

    // Set up auto-refresh every 60 seconds as fallback
    refreshInterval = setInterval(loadLiveData, 60000);
  });

  onDestroy(() => {
    if (refreshInterval) clearInterval(refreshInterval);
    streamEventsSub.unlisten();
    trackListSub.unlisten();
  });

  function startSubscriptions() {
    const operationalStreamId = resolveOperationalStreamId({
      routeParamId: streamId,
      streamUuid: stream?.streamId,
    });
    if (!operationalStreamId) return;

    streamEventsSub.listen({ streamId: operationalStreamId });
    trackListSub.listen({ streamId: operationalStreamId });
  }

  function handleStreamEvent(event: NonNullable<StreamEvents$result["liveStreamEvents"]>) {
    if (event.type === "STREAM_START") {
      toast.success("Stream is now live!");
      return;
    }

    if (event.type === "STREAM_END") {
      toast.info("Stream ended");
    }
  }

  async function loadStreamData() {
    try {
      error = null;

      const analyticsStreamId = resolveOperationalStreamId({
        routeParamId: streamId,
        streamUuid: stream?.streamId,
      });
      const result = await streamStore.fetch({
        variables: { id: streamId, streamId: analyticsStreamId || streamId },
      });

      if (!result.data?.stream) {
        error = "Stream not found";
        return;
      }
      const fetchedStreamId = (result.data.stream as { streamId?: string | null }).streamId;
      const resolvedStreamId = resolveOperationalStreamId({
        routeParamId: streamId,
        streamUuid: fetchedStreamId,
      });

      if (!resolvedStreamId) {
        error = "Unable to resolve stream identifier";
        return;
      }

      await Promise.all([
        streamKeysStore.fetch({ variables: { streamId: resolvedStreamId } }),
        pushTargetsStore.fetch({ variables: { streamId } }),
        storageArtifactsStore.fetch({
          policy: "NetworkOnly",
          variables: {
            input: {
              first: 100,
              offset: 0,
              streamId: resolvedStreamId,
              kinds: storageKindsForStream(),
              sort: "CREATED_AT",
              direction: "DESC",
            },
          },
        }),
      ]);

      startSubscriptions();
    } catch (err) {
      console.error("Failed to load stream data:", err);
      error = "Failed to load stream data";
    }
  }

  async function loadLiveData() {
    try {
      const analyticsStreamId = resolveOperationalStreamId({
        routeParamId: streamId,
        streamUuid: stream?.streamId,
      });
      await streamStore.fetch({ variables: { id: streamId, streamId: analyticsStreamId } });
    } catch (err) {
      console.error("Failed to refresh live data:", err);
    }
  }

  async function handleRefreshStreamKey() {
    if (!stream) return;

    try {
      actionLoading.refreshKey = true;
      const result = await refreshStreamKeyMutation.mutate({ id: streamId });
      if (result.data?.refreshStreamKey?.__typename === "Stream") {
        toast.success("Stream key refreshed successfully!");
        const analyticsStreamId = resolveOperationalStreamId({
          routeParamId: streamId,
          streamUuid: stream?.streamId,
        });
        if (analyticsStreamId) {
          await streamStore.fetch({ variables: { id: streamId, streamId: analyticsStreamId } });
        }
      } else {
        const errorResult = result.data?.refreshStreamKey as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to refresh stream key");
      }
    } catch (err) {
      console.error("Failed to refresh stream key:", err);
      toast.error("Failed to refresh stream key");
    } finally {
      actionLoading.refreshKey = false;
    }
  }

  async function handleEditStream(formData: {
    name?: string;
    description?: string;
    record?: boolean;
    pullSourceUri?: string;
    pullSourceEnabled?: boolean;
    pullSourceAllowedClusterIds?: string;
    pullSourceAllowedClustersDirty?: boolean;
    dvrChapterMode?: "WINDOW_SIZED" | "FIXED_INTERVAL" | "NONE" | null;
    dvrChapterIntervalSeconds?: number | null;
    retentionOverrides?: {
      dvr?: { value: number } | { clear: true };
      clip?: { value: number } | { clear: true };
    };
  }) {
    if (!stream) return;

    try {
      actionLoading.editStream = true;
      const pullURIChanged = !!formData.pullSourceUri?.trim();
      const pullEnabledChanged = formData.pullSourceEnabled !== stream.pullSource?.enabled;
      const pullAllowedDirty = !!formData.pullSourceAllowedClustersDirty;
      // Only send pullSource when something actually changed. The wrapper
      // contract is: any field we omit is preserved; any field we set is
      // replaced. For allowed_clusters specifically: send the wrapper only
      // when the user edited the field (dirty flag) — otherwise omit so the
      // server preserves the existing pin.
      const pullSource =
        stream.ingestMode === "PULL" && (pullURIChanged || pullEnabledChanged || pullAllowedDirty)
          ? {
              sourceUri: pullURIChanged ? formData.pullSourceUri!.trim() : undefined,
              enabled: pullEnabledChanged ? (formData.pullSourceEnabled ?? true) : undefined,
              allowedClusters: pullAllowedDirty
                ? {
                    clusterIds: (formData.pullSourceAllowedClusterIds ?? "")
                      .split(",")
                      .map((s) => s.trim())
                      .filter((s) => s.length > 0),
                  }
                : undefined,
            }
          : undefined;
      const input = {
        name: formData.name,
        description: formData.description,
        record: formData.record,
        ingestMode: stream.ingestMode,
        pullSource,
        dvrChapterMode: formData.dvrChapterMode,
        dvrChapterIntervalSeconds: formData.dvrChapterIntervalSeconds,
      };
      const result = await updateStreamMutation.mutate({
        id: streamId,
        input,
      });
      if (result.data?.updateStream?.__typename !== "Stream") {
        const errorResult = result.data?.updateStream as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to update stream");
        return;
      }

      // Apply per-stream retention overrides as a separate RPC. The two
      // mutations don't share an input shape and clear is per-field — keep
      // them split so the wire contract stays explicit. Failure here
      // surfaces but doesn't roll back the updateStream above (Commodore
      // accepts both writes independently).
      if (formData.retentionOverrides && stream?.streamId) {
        const overrideInput: {
          streamId: string;
          dvrRetentionDaysOverride?: number;
          clipRetentionDaysOverride?: number;
          clearDvrRetentionOverride?: boolean;
          clearClipRetentionOverride?: boolean;
        } = { streamId: stream.streamId };
        if (formData.retentionOverrides.dvr) {
          if ("clear" in formData.retentionOverrides.dvr) {
            overrideInput.clearDvrRetentionOverride = true;
          } else {
            overrideInput.dvrRetentionDaysOverride = formData.retentionOverrides.dvr.value;
          }
        }
        if (formData.retentionOverrides.clip) {
          if ("clear" in formData.retentionOverrides.clip) {
            overrideInput.clearClipRetentionOverride = true;
          } else {
            overrideInput.clipRetentionDaysOverride = formData.retentionOverrides.clip.value;
          }
        }
        const overrideResult = await setStreamRetentionOverridesMutation.mutate({
          input: overrideInput,
        });
        const overrideData = overrideResult.data?.setStreamRetentionOverrides;
        if (overrideData?.__typename !== "StreamRetentionOverrides") {
          const errorResult = overrideData as unknown as { message?: string };
          toast.error(errorResult?.message || "Failed to update retention overrides");
          return;
        }
      }

      showEditModal = false;
      toast.success("Stream updated successfully!");
      const analyticsStreamId = resolveOperationalStreamId({
        routeParamId: streamId,
        streamUuid: stream?.streamId,
      });
      if (analyticsStreamId) {
        await streamStore.fetch({ variables: { id: streamId, streamId: analyticsStreamId } });
      }
    } catch (err) {
      console.error("Failed to update stream:", err);
      toast.error("Failed to update stream");
    } finally {
      actionLoading.editStream = false;
    }
  }

  async function handleDeleteStream() {
    if (!stream) return;

    try {
      actionLoading.deleteStream = true;
      const result = await deleteStreamMutation.mutate({ id: streamId });
      if (result.data?.deleteStream?.__typename === "DeleteSuccess") {
        // The two-phase deletion may still be finalizing (pending) when the serving cell was briefly unreachable;
        // it converges automatically. Tell the user rather than implying an instant, complete delete.
        if (result.data.deleteStream.pending) {
          toast.success("Stream deletion is finalizing and will complete shortly.");
        } else {
          toast.success("Stream deleted.");
        }
        goto(resolve("/streams"));
      } else {
        const errorResult = result.data?.deleteStream as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to delete stream");
        actionLoading.deleteStream = false;
      }
    } catch (err) {
      console.error("Failed to delete stream:", err);
      toast.error("Failed to delete stream");
      actionLoading.deleteStream = false;
    }
  }

  async function handleCreateStreamKey(formData: { keyName: string; isActive: boolean }) {
    try {
      actionLoading.createKey = true;
      const result = await createStreamKeyMutation.mutate({
        streamId,
        input: { name: formData.keyName },
      });
      if (result.data?.createStreamKey?.__typename === "StreamKey") {
        showCreateKeyModal = false;
        toast.success("Stream key created successfully!");
        await streamKeysStore.fetch({ variables: { streamId } });
      } else {
        const errorResult = result.data?.createStreamKey as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to create stream key");
      }
    } catch (err) {
      console.error("Failed to create stream key:", err);
      toast.error("Failed to create stream key");
    } finally {
      actionLoading.createKey = false;
    }
  }

  async function handleDeleteStreamKey(keyId: string) {
    try {
      actionLoading.deleteKey = keyId;
      const result = await deleteStreamKeyMutation.mutate({ streamId, keyId });
      if (result.data?.deleteStreamKey?.__typename === "DeleteSuccess") {
        toast.success("Stream key deleted successfully!");
        await streamKeysStore.fetch({ variables: { streamId } });
      } else {
        const errorResult = result.data?.deleteStreamKey as unknown as { message?: string };
        toast.error(errorResult?.message || "Failed to delete stream key");
      }
    } catch (err) {
      console.error("Failed to delete stream key:", err);
      toast.error("Failed to delete stream key");
    } finally {
      actionLoading.deleteKey = null;
    }
  }

  async function handleCreatePushTarget(formData: {
    platform?: string;
    name: string;
    targetUri: string;
  }) {
    try {
      actionLoading.createPushTarget = true;
      await createPushTargetMutation.mutate({
        streamId,
        input: formData,
      });
      showCreatePushTargetModal = false;
      toast.success("Push target added");
      await pushTargetsStore.fetch({ variables: { streamId } });
    } catch (err) {
      console.error("Failed to create push target:", err);
      toast.error("Failed to add push target");
    } finally {
      actionLoading.createPushTarget = false;
    }
  }

  async function handleUpdatePushTarget(updates: {
    name?: string;
    targetUri?: string;
    isEnabled?: boolean;
  }) {
    if (!editingPushTarget) return;
    try {
      actionLoading.updatePushTarget = true;
      await updatePushTargetMutation.mutate({
        id: editingPushTarget.id,
        input: updates,
      });
      showEditPushTargetModal = false;
      editingPushTarget = null;
      toast.success("Push target updated");
      await pushTargetsStore.fetch({ variables: { streamId } });
    } catch (err) {
      console.error("Failed to update push target:", err);
      toast.error("Failed to update push target");
    } finally {
      actionLoading.updatePushTarget = false;
    }
  }

  async function handleTogglePushTarget(target: (typeof pushTargets)[number]) {
    try {
      actionLoading.togglePushTarget = target.id;
      await updatePushTargetMutation.mutate({
        id: target.id,
        input: { isEnabled: !target.isEnabled },
      });
      toast.success(target.isEnabled ? "Push target disabled" : "Push target enabled");
      await pushTargetsStore.fetch({ variables: { streamId } });
    } catch (err) {
      console.error("Failed to toggle push target:", err);
      toast.error("Failed to toggle push target");
    } finally {
      actionLoading.togglePushTarget = null;
    }
  }

  async function handleDeletePushTarget(targetId: string) {
    try {
      actionLoading.deletePushTarget = targetId;
      await deletePushTargetMutation.mutate({ id: targetId });
      toast.success("Push target deleted");
      await pushTargetsStore.fetch({ variables: { streamId } });
    } catch (err) {
      console.error("Failed to delete push target:", err);
      toast.error("Failed to delete push target");
    } finally {
      actionLoading.deletePushTarget = null;
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text).then(() => {
      toast.success("Copied to clipboard");
    });
  }

  function navigateBack() {
    goto(resolve("/streams"));
  }

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const EditIcon = getIconComponent("Edit");
  const Trash2Icon = getIconComponent("Trash2");
  const CircleIcon = getIconComponent("Circle");
  const InfoIcon = getIconComponent("Info");
  const SettingsIcon = getIconComponent("Settings");
  const BarChart2Icon = getIconComponent("BarChart2");
  const HeartIcon = getIconComponent("Heart");
  const _KeyIcon = getIconComponent("Key");
  const VideoIcon = getIconComponent("Video");
  const PlayIcon = getIconComponent("Play");
  const ShieldCheckIcon = getIconComponent("ShieldCheck");
</script>

<svelte:head>
  <title>Stream Details - {stream?.name || "Loading..."} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-4">
        <Button variant="ghost" size="icon" class="rounded-full" onclick={navigateBack}>
          <ArrowLeftIcon class="w-5 h-5" />
        </Button>

        <div>
          <h1 class="text-xl font-bold text-foreground">Stream Details</h1>
          <div class="flex items-center gap-2 mt-0.5">
            <span class="text-sm font-medium text-foreground">
              {stream?.name || "Loading..."}
            </span>
            <span class="text-xs text-muted-foreground">•</span>
            <span class="text-xs text-muted-foreground font-mono">
              {stream?.id?.slice(0, 8) || ""}...
            </span>
            {#if stream}
              <!-- Status Badge -->
              <span
                class="flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-medium {isLive
                  ? 'bg-success/20 text-success'
                  : 'bg-muted text-muted-foreground'}"
              >
                <CircleIcon class="w-1.5 h-1.5 {isLive ? 'fill-current animate-pulse' : ''}" />
                {isLive ? "LIVE" : "OFFLINE"}
              </span>

              {#if stream.record}
                <span
                  class="flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-medium bg-error/20 text-error"
                >
                  <CircleIcon class="w-1.5 h-1.5 fill-current" />
                  REC
                </span>
              {/if}
            {/if}
          </div>
        </div>
      </div>

      {#if stream && !loading}
        <div class="flex items-center space-x-2">
          <!-- Analytics Button -->
          <Button
            variant="ghost"
            size="sm"
            class="hidden sm:flex gap-2"
            onclick={() => goto(resolve(`/streams/${streamId}/analytics`))}
          >
            <BarChart2Icon class="w-4 h-4" />
            Analytics
          </Button>

          <!-- Actions Dropdown -->
          <DropdownMenu>
            <DropdownMenuTrigger>
              {#snippet child({ props })}
                <Button variant="ghost" size="icon" {...props}>
                  <MoreVertical class="w-4 h-4" />
                </Button>
              {/snippet}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel>View</DropdownMenuLabel>
              <DropdownMenuItem onclick={() => goto(resolve(`/streams/${streamId}/analytics`))}>
                <BarChart2Icon class="w-4 h-4 mr-2" />
                Analytics
              </DropdownMenuItem>
              <DropdownMenuItem onclick={() => goto(resolve(`/streams/${streamId}/health`))}>
                <HeartIcon class="w-4 h-4 mr-2" />
                Health
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuLabel>Actions</DropdownMenuLabel>
              <DropdownMenuItem onclick={() => (showEditModal = true)}>
                <EditIcon class="w-4 h-4 mr-2" />
                Edit Stream
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                class="text-destructive focus:text-destructive"
                onclick={() => (showDeleteModal = true)}
              >
                <Trash2Icon class="w-4 h-4 mr-2" />
                Delete Stream
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      {/if}
    </div>
  </div>

  <!-- Main Content Area -->
  <div class="flex-1 flex overflow-hidden">
    {#if loading}
      <div class="flex-1 p-6">
        <LoadingCard variant="analytics" />
      </div>
    {:else if error}
      {@const AlertTriangleIcon = getIconComponent("AlertTriangle")}
      <div class="flex-1 p-6">
        <div class="border border-error/30 bg-error/10 p-8 text-center">
          <AlertTriangleIcon class="w-8 h-8 text-error mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-error mb-2">Error Loading Stream</h3>
          <p class="text-error mb-4">{error}</p>
          <Button variant="outline" onclick={loadStreamData}>Retry</Button>
        </div>
      </div>
    {:else if stream}
      <!-- Main Content (scrollable) -->
      <div class="flex-1 overflow-y-auto">
        <div class="flex flex-col">
          <!-- Stream Overview Cards -->
          <div
            class="grid grid-cols-1 md:grid-cols-3 divide-y md:divide-y-0 md:divide-x divide-[hsl(var(--tn-fg-gutter)/0.3)] bg-background"
          >
            <StreamStatusCard {stream} />
            <StreamKeyCard
              {stream}
              loading={actionLoading.refreshKey}
              onRefresh={handleRefreshStreamKey}
              onCopy={copyToClipboard}
            />
            <StreamPlaybackCard {stream} onCopy={copyToClipboard} />
          </div>

          <SectionDivider showBar={false} class="p-0" />

          <!-- Tabbed Content -->
          <div class="slab border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <Tabs value="overview" class="w-full">
              <TabsList
                class="flex w-full rounded-none p-0 h-auto bg-[hsl(var(--tn-bg-dark)/0.5)] border-b border-[hsl(var(--tn-fg-gutter)/0.3)] justify-start overflow-x-auto items-center"
              >
                <TabsTrigger
                  value="overview"
                  class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                >
                  <InfoIcon class="w-4 h-4" />
                  Overview
                </TabsTrigger>
                <TabsTrigger
                  value="ingest"
                  class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                >
                  <SettingsIcon class="w-4 h-4" />
                  Ingest
                </TabsTrigger>
                <TabsTrigger
                  value="artefacts"
                  class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                >
                  <VideoIcon class="w-4 h-4" />
                  Artefacts ({recordings.length + clips.length + vodArtifacts.length})
                </TabsTrigger>
                <TabsTrigger
                  value="multistream"
                  class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                >
                  {@const RadioIcon = getIconComponent("Radio")}
                  <RadioIcon class="w-4 h-4" />
                  Multistream ({pushTargets.length})
                </TabsTrigger>
                <TabsTrigger
                  value="playback"
                  class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                >
                  <PlayIcon class="w-4 h-4" />
                  Playback
                </TabsTrigger>
                <TabsTrigger
                  value="playback-auth"
                  class="gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors"
                >
                  <ShieldCheckIcon class="w-4 h-4" />
                  Playback Auth{#if stream?.playbackPolicy && stream.playbackPolicy.type !== "PUBLIC"}
                    ·
                    <span class="text-info">{stream.playbackPolicy.type}</span>{/if}
                </TabsTrigger>
              </TabsList>

              <TabsContent value="overview" class="p-0 min-h-[20rem]">
                <OverviewTabPanel
                  {stream}
                  {streamKeys}
                  {recordings}
                  tracks={currentTracks ?? fallbackTracks}
                />
              </TabsContent>

              <TabsContent value="ingest" class="p-0 min-h-[20rem]">
                <StreamSetupPanel
                  {stream}
                  {streamKeys}
                  onRefreshKey={handleRefreshStreamKey}
                  refreshingKey={actionLoading.refreshKey}
                  onCreateKey={() => (showCreateKeyModal = true)}
                  onCopyKey={copyToClipboard}
                  onDeleteKey={handleDeleteStreamKey}
                  deleteLoading={actionLoading.deleteKey}
                />
              </TabsContent>

              <TabsContent value="artefacts" class="p-0 min-h-[20rem]">
                <ArtefactsTabPanel
                  {recordings}
                  {clips}
                  {vodArtifacts}
                  onEnableRecording={() => (showEditModal = true)}
                />
              </TabsContent>

              <TabsContent value="multistream" class="p-0 min-h-[20rem]">
                <PushTargetsTabPanel
                  {pushTargets}
                  onAdd={() => (showCreatePushTargetModal = true)}
                  onEdit={(target) => {
                    editingPushTarget = target;
                    showEditPushTargetModal = true;
                  }}
                  onToggle={handleTogglePushTarget}
                  onDelete={handleDeletePushTarget}
                  deleteLoading={actionLoading.deletePushTarget}
                  toggleLoading={actionLoading.togglePushTarget}
                />
              </TabsContent>

              <TabsContent value="playback" class="p-0 min-h-[20rem]">
                <PlaybackTabPanel playbackId={stream?.playbackId} />
              </TabsContent>

              <TabsContent value="playback-auth" class="p-0 min-h-[20rem]">
                <PlaybackAuthTabPanel
                  streamId={stream?.id ?? ""}
                  playbackId={stream?.playbackId ?? ""}
                  playbackPolicy={stream?.playbackPolicy ?? null}
                  onSaved={() => streamStore.fetch({ policy: "NetworkOnly" })}
                />
              </TabsContent>
            </Tabs>
          </div>
        </div>
      </div>
    {/if}
  </div>

  <!-- Modals -->
  <StreamEditModal
    bind:open={showEditModal}
    {stream}
    loading={actionLoading.editStream}
    onSave={handleEditStream}
  />
  <StreamDeleteModal
    bind:open={showDeleteModal}
    streamName={stream?.name || ""}
    loading={actionLoading.deleteStream}
    onConfirm={handleDeleteStream}
  />
  <StreamCreateKeyModal
    bind:open={showCreateKeyModal}
    loading={actionLoading.createKey}
    onCreate={handleCreateStreamKey}
  />
  <PushTargetCreateModal
    bind:open={showCreatePushTargetModal}
    loading={actionLoading.createPushTarget}
    onCreate={handleCreatePushTarget}
  />
  <PushTargetEditModal
    bind:open={showEditPushTargetModal}
    target={editingPushTarget}
    loading={actionLoading.updatePushTarget}
    onUpdate={handleUpdatePushTarget}
  />
</div>
