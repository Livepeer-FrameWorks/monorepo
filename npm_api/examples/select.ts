// Select fields beyond the generated documents' default selection.
import { createClient } from "@livepeer-frameworks/api";
import { createSelectClient } from "@livepeer-frameworks/api/select";
import { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";

const token = () => process.env.FRAMEWORKS_API_TOKEN;
const select = createSelectClient(
  createClient({ url: "https://bridge.example.com/graphql", token }),
  // Only needed for select.subscription.
  { subscriptions: createSubscriptionClient({ url: "wss://bridge.example.com/graphql/ws", token }) }
);

const { stream } = await select.query({
  stream: {
    __args: { id: "c3RyZWFtOjE" },
    name: true,
    metrics: { status: true, currentViewers: true, startedAt: true },
  },
});
if (stream) {
  console.log(
    `${stream.name}: ${stream.metrics?.status}, ${stream.metrics?.currentViewers} viewers`
  );
}

const { createClip } = await select.mutation({
  createClip: {
    __args: {
      input: { streamId: "c3RyZWFtOjE", title: "Last 30 s", mode: "CLIP_NOW", duration: 30 },
    },
    __typename: true,
    on_Clip: { id: true, status: true },
    on_ValidationError: { message: true, field: true },
  },
});
if (createClip.__typename === "Clip") {
  console.log(`clip ${createClip.id} is ${createClip.status}`);
}

for await (const { tenantEvents: event } of select.subscription({
  tenantEvents: {
    __args: { types: ["stream.live"] },
    time: true,
    data: { __typename: true, on_StreamLive: { streamId: true } },
  },
})) {
  if (event.data.__typename === "StreamLive") {
    console.log(`${event.data.streamId} went live at ${event.time}`);
  }
}
