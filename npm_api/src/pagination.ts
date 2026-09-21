/**
 * Async iterators over the three pagination shapes of the API. Each takes a
 * function that fetches one page for a page request, so any operation with
 * that shape can be walked:
 *
 *     for await (const stream of paginateRelay((page) =>
 *       client.request(ListStreamsDocument, { page }).then((d) => d.streamsConnection)
 *     )) { ... }
 */

export interface PaginateOptions {
  /** Rows requested per page. */
  pageSize?: number;
  /** Stop after this many rows. */
  maxItems?: number;
}

export interface RelayPageRequest {
  first: number;
  after: string | null;
}

export interface RelayPage<T> {
  nodes: ReadonlyArray<T>;
  pageInfo: { hasNextPage: boolean; endCursor: string | null };
}

export interface OffsetPageRequest {
  first: number;
  offset: number;
}

export interface OffsetPage<T> {
  nodes: ReadonlyArray<T>;
  hasNextPage: boolean;
}

export interface PageTokenRequest {
  pageSize: number;
  pageToken: string | null;
}

export interface TokenPage<T> {
  items: ReadonlyArray<T>;
  nextPageToken: string | null | undefined;
}

const defaultPageSize = 50;

/** Walks a relay connection (first/after, pageInfo.endCursor), e.g. streamsConnection. */
export async function* paginateRelay<T>(
  fetchPage: (request: RelayPageRequest) => Promise<RelayPage<T>>,
  options: PaginateOptions = {}
): AsyncGenerator<T, void, undefined> {
  const first = options.pageSize ?? defaultPageSize;
  let after: string | null = null;
  let yielded = 0;
  for (;;) {
    const page = await fetchPage({ first, after });
    for (const node of page.nodes) {
      if (options.maxItems !== undefined && yielded >= options.maxItems) {
        return;
      }
      yield node;
      yielded++;
    }
    if (options.maxItems !== undefined && yielded >= options.maxItems) {
      return;
    }
    if (!page.pageInfo.hasNextPage || !page.pageInfo.endCursor || page.nodes.length === 0) {
      return;
    }
    after = page.pageInfo.endCursor;
  }
}

/** Walks an offset-paged list (first/offset, hasNextPage), e.g. storageArtifactsConnection. */
export async function* paginateOffset<T>(
  fetchPage: (request: OffsetPageRequest) => Promise<OffsetPage<T>>,
  options: PaginateOptions = {}
): AsyncGenerator<T, void, undefined> {
  const first = options.pageSize ?? defaultPageSize;
  let offset = 0;
  let yielded = 0;
  for (;;) {
    const page = await fetchPage({ first, offset });
    for (const node of page.nodes) {
      if (options.maxItems !== undefined && yielded >= options.maxItems) {
        return;
      }
      yield node;
      yielded++;
    }
    if (options.maxItems !== undefined && yielded >= options.maxItems) {
      return;
    }
    if (!page.hasNextPage || page.nodes.length === 0) {
      return;
    }
    offset += page.nodes.length;
  }
}

/** Walks a page-token list (pageSize/pageToken, nextPageToken), e.g. dvrChapters. */
export async function* paginatePageToken<T>(
  fetchPage: (request: PageTokenRequest) => Promise<TokenPage<T>>,
  options: PaginateOptions = {}
): AsyncGenerator<T, void, undefined> {
  const pageSize = options.pageSize ?? defaultPageSize;
  let pageToken: string | null = null;
  let yielded = 0;
  for (;;) {
    const page = await fetchPage({ pageSize, pageToken });
    for (const item of page.items) {
      if (options.maxItems !== undefined && yielded >= options.maxItems) {
        return;
      }
      yield item;
      yielded++;
    }
    if (options.maxItems !== undefined && yielded >= options.maxItems) {
      return;
    }
    if (!page.nextPageToken || page.items.length === 0) {
      return;
    }
    pageToken = page.nextPageToken;
  }
}
