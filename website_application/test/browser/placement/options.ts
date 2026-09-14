export class GetMediaPlacementOptionsStore {
  async fetch() {
    return {
      data: {
        mediaPlacementOptions: {
          __typename: "MediaPlacementOptionsConnection",
          nodes: [
            {
              id: "fixture-eu",
              name: "Fixture: my EU cluster",
              kind: "CLUSTER",
              eligible: true,
              region: "eu-west",
              reason: null,
            },
            {
              id: "fixture-us",
              name: "Fixture: marketplace US cluster",
              kind: "CLUSTER",
              eligible: false,
              region: "us-east",
              reason: "Fixture: temporarily unavailable",
            },
          ],
          pageInfo: { hasNextPage: false, endCursor: null },
        },
      },
    };
  }
}

export class GetStreamingConfigStore {
  async fetch() {
    return { data: { streamingConfig: null } };
  }
}

export class ResolveIngestDestinationStore {
  async fetch() {
    throw new Error("Publisher ingest resolution is not part of the source-mode fixture.");
  }
}
