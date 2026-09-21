// Create a stream, then list every stream page by page.
import {
  createClient,
  CreateStreamDocument,
  expectResult,
  ListStreamsDocument,
  paginateRelay,
  ResultError,
} from "@livepeer-frameworks/api";

const client = createClient({
  url: "https://bridge.example.com/graphql",
  token: process.env.FRAMEWORKS_API_TOKEN,
});

try {
  const created = await client.request(CreateStreamDocument, {
    input: { name: "Launch stream", record: true },
  });
  const stream = expectResult(created.createStream, "Stream");
  console.log(
    `created ${stream.name}: stream key ${stream.streamKey}, playback ID ${stream.playbackId}`
  );
} catch (err) {
  if (err instanceof ResultError && err.typename === "ValidationError") {
    console.error(`invalid ${err.field}: ${err.message}`);
  }
  throw err;
}

const streams = paginateRelay(
  (page) => client.request(ListStreamsDocument, { page }).then((data) => data.streamsConnection),
  { pageSize: 50 }
);
for await (const stream of streams) {
  console.log(stream.name, stream.metrics?.isLive ? "live" : "offline");
}
