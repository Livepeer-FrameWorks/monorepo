// Receive FrameWorks webhooks: verify the signature on the raw body, then
// handle the typed event.
import { createServer } from "node:http";

import { createWebhookReceiver, WebhookVerificationError } from "@livepeer-frameworks/api/webhooks";

const receiver = createWebhookReceiver(process.env.FRAMEWORKS_WEBHOOK_SECRET ?? "");

createServer((req, res) => {
  const chunks: Buffer[] = [];
  req.on("data", (chunk: Buffer) => chunks.push(chunk));
  req.on("end", () => {
    let event;
    try {
      event = receiver.receive(Buffer.concat(chunks), req.headers);
    } catch (err) {
      res.writeHead(err instanceof WebhookVerificationError ? 401 : 400).end();
      return;
    }
    if (event.known) {
      switch (event.type) {
        case "stream.live":
          console.log(`stream ${event.data.streamId} is live`);
          break;
        case "clip.ready":
          console.log(
            `clip ${event.data.artifact?.artifactId} ready, ${event.data.sizeBytes} bytes`
          );
          break;
      }
    } else {
      console.log(`event type ${event.type} is newer than this SDK`);
    }
    res.writeHead(204).end();
  });
}).listen(8080);
