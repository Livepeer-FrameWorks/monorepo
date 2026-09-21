// Upload a local file as a VOD asset.
import { openAsBlob } from "node:fs";

import { createClient, uploadVod } from "@livepeer-frameworks/api";

const client = createClient({
  url: "https://bridge.example.com/graphql",
  token: process.env.FRAMEWORKS_API_TOKEN,
});

const file = await openAsBlob("./talk.mp4");
const asset = await uploadVod(client, file, {
  filename: "talk.mp4",
  contentType: "video/mp4",
  title: "Conference talk",
  concurrency: 4,
  onProgress: (sent, total) => console.log(`${Math.round((sent / total) * 100)}%`),
});
console.log(`uploaded ${asset.id}; playback ID ${asset.playbackId}, status ${asset.status}`);
