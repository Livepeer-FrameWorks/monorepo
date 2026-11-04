// Create a stream, then list every stream page by page.
import { createClient, expectResult, paginateRelay, ResultError } from "@livepeer-frameworks/api";
import { getSdk } from "@livepeer-frameworks/api/operations";

const client = createClient({
  url: "https://bridge.example.com/graphql",
  token: process.env.FRAMEWORKS_API_TOKEN,
});
const sdk = getSdk(client);

try {
  const created = await sdk.createStream({
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
  (page) => sdk.listStreams({ page }).then((data) => data.streamsConnection),
  { pageSize: 50 }
);
for await (const stream of streams) {
  console.log(stream.name, stream.playbackId);
}
