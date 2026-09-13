interface CatalogPage<Node> {
  nodes: Node[];
  hasNextPage: boolean;
}

// Refresh the loaded window atomically: later pages, filter changes, navigation and
// tenant switches must never be overwritten by a background response.
export function createCatalogRefresh<Node, Page extends CatalogPage<Node>>(options: {
  scope: () => string | null;
  count: () => number;
  pageSize: number;
  key: (node: Node) => string;
  fetchPage: (offset: number) => Promise<Page>;
  apply: (nodes: Node[], lastPage: Page) => void;
  onError: (error: unknown) => void;
}) {
  let running = false;
  let disposed = false;
  return {
    async run() {
      const scope = options.scope();
      if (disposed || running || scope === null) return;
      running = true;
      const current = () => !disposed && options.scope() === scope;
      try {
        const count = Math.max(options.pageSize, options.count());
        const nodes: Node[] = [];
        const seen = new Set<string>();
        for (let offset = 0; offset < count; offset += options.pageSize) {
          if (!current()) return;
          const page = await options.fetchPage(offset);
          if (!current()) return;
          for (const node of page.nodes) {
            const key = options.key(node);
            if (!seen.has(key)) {
              seen.add(key);
              nodes.push(node);
            }
          }
          if (!page.hasNextPage || offset + options.pageSize >= count) {
            options.apply(nodes, page);
            return;
          }
        }
      } catch (error) {
        if (current()) options.onError(error);
      } finally {
        running = false;
      }
    },
    dispose() {
      disposed = true;
    },
  };
}
