import { getGraphqlHttpUrl } from "$lib/config";
import type { ContentEndpoints, PlayerMetadata } from "@livepeer-frameworks/player-svelte";

export type DVRChapterMode = "WINDOW_SIZED" | "FIXED_INTERVAL" | "EXPLICIT_RANGE" | "NONE";

export interface DVRChapterRef {
  chapterId: string;
  mode: DVRChapterMode;
  intervalSeconds?: number | null;
  startMs: number;
  endMs: number;
  isCurrent: boolean;
  manifestS3Key?: string | null;
  hasGaps: boolean;
  segmentCount: number;
}

export interface DVRChapter {
  chapterId: string;
  manifestS3Key: string;
  manifestUrl: string;
  isCurrent: boolean;
  hasGaps: boolean;
  segmentCount: number;
}

interface ViewerEndpoint {
  nodeId: string;
  baseUrl?: string | null;
  protocol: string;
  url: string;
  geoDistance?: number | null;
  loadScore?: number | null;
  outputs?: Record<string, unknown> | null;
}

interface GraphQLResponse<T> {
  data?: T;
  errors?: Array<{ message?: string }>;
}

async function graphqlRequest<T>(query: string, variables: Record<string, unknown>): Promise<T> {
  const response = await fetch(getGraphqlHttpUrl(), {
    method: "POST",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ query, variables }),
  });

  if (!response.ok) {
    throw new Error(`GraphQL request failed with ${response.status}`);
  }

  const payload = (await response.json()) as GraphQLResponse<T>;
  if (payload.errors?.length) {
    throw new Error(payload.errors[0]?.message || "GraphQL request failed");
  }
  if (!payload.data) {
    throw new Error("GraphQL response did not include data");
  }
  return payload.data;
}

export async function listDvrChapters(options: {
  dvrId: string;
  mode?: DVRChapterMode;
  intervalSeconds?: number | null;
  rangeStartMs?: number;
  rangeEndMs?: number;
  pageSize?: number;
  pageToken?: string | null;
}): Promise<{ chapters: DVRChapterRef[]; nextPageToken?: string | null }> {
  const data = await graphqlRequest<{
    dvrChapters: { chapters: DVRChapterRef[]; nextPageToken?: string | null } | null;
  }>(
    `
      query WebDVRChapters(
        $dvrId: ID!
        $mode: DVRChapterMode
        $intervalSeconds: Int
        $rangeStartMs: Float
        $rangeEndMs: Float
        $pageSize: Int
        $pageToken: String
      ) {
        dvrChapters(
          dvrId: $dvrId
          mode: $mode
          intervalSeconds: $intervalSeconds
          rangeStartMs: $rangeStartMs
          rangeEndMs: $rangeEndMs
          pageSize: $pageSize
          pageToken: $pageToken
        ) {
          chapters {
            chapterId
            mode
            intervalSeconds
            startMs
            endMs
            isCurrent
            manifestS3Key
            hasGaps
            segmentCount
          }
          nextPageToken
        }
      }
    `,
    options
  );

  return data.dvrChapters || { chapters: [], nextPageToken: null };
}

export async function retrieveDvrChapter(options: {
  dvrId: string;
  mode: DVRChapterMode;
  intervalSeconds?: number | null;
  startMs: number;
  endMs: number;
}): Promise<DVRChapter> {
  const data = await graphqlRequest<{ dvrChapter: DVRChapter | null }>(
    `
      query WebDVRChapter(
        $dvrId: ID!
        $mode: DVRChapterMode!
        $intervalSeconds: Int
        $startMs: Float!
        $endMs: Float!
      ) {
        dvrChapter(
          dvrId: $dvrId
          mode: $mode
          intervalSeconds: $intervalSeconds
          startMs: $startMs
          endMs: $endMs
        ) {
          chapterId
          manifestS3Key
          manifestUrl
          isCurrent
          hasGaps
          segmentCount
        }
      }
    `,
    options
  );

  if (!data.dvrChapter) {
    throw new Error("DVR chapter was not found");
  }
  return data.dvrChapter;
}

export async function resolveDvrChapterPlayback(playbackId: string): Promise<{
  contentId: string;
  endpoints: ContentEndpoints;
  metadata?: PlayerMetadata | null;
}> {
  const contentId = playbackId;
  const data = await graphqlRequest<{
    resolveViewerEndpoint: {
      primary: ViewerEndpoint;
      fallbacks: ViewerEndpoint[];
      metadata?: PlayerMetadata | null;
    } | null;
  }>(
    `
      query WebDVRChapterPlayback($contentId: String!) {
        resolveViewerEndpoint(contentId: $contentId) {
          primary {
            nodeId
            baseUrl
            protocol
            url
            geoDistance
            loadScore
            outputs
          }
          fallbacks {
            nodeId
            baseUrl
            protocol
            url
            geoDistance
            loadScore
            outputs
          }
          metadata {
            status
            isLive
            viewers
            bufferState
            protocolHints
            dvrStatus
            dvrSourceUri
            contentId
            contentType
            title
            description
            durationSeconds
            recordingSizeBytes
            clipSource
            createdAt
          }
        }
      }
    `,
    { contentId }
  );

  if (!data.resolveViewerEndpoint?.primary) {
    throw new Error("DVR chapter playback endpoint was not available");
  }
  return {
    contentId,
    endpoints: {
      primary: data.resolveViewerEndpoint.primary,
      fallbacks: data.resolveViewerEndpoint.fallbacks || [],
      metadata: data.resolveViewerEndpoint.metadata ?? undefined,
    } as ContentEndpoints,
    metadata: data.resolveViewerEndpoint.metadata ?? null,
  };
}
