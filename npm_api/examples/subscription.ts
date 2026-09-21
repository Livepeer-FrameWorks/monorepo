// Follow a stream's lifecycle and recording events as they happen.
import { TenantEventsDocument } from "@livepeer-frameworks/api";
import { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";

const subscriptions = createSubscriptionClient({
  url: "wss://bridge.example.com/graphql/ws",
  // Read again for every connection, so a refreshed token is used after a reconnect.
  token: () => process.env.FRAMEWORKS_API_TOKEN,
});

for await (const { tenantEvents: event } of subscriptions.subscribe(TenantEventsDocument, {
  types: ["stream.live", "stream.idle", "recording.ready"],
})) {
  switch (event.data.__typename) {
    case "StreamLive":
      console.log(`${event.data.streamId} went live at ${event.time}`);
      break;
    case "RecordingReady":
      console.log(`recording ${event.data.artifact?.artifactId} is ready`);
      break;
    default:
      console.log(event.type);
  }
}
