# @livepeer-frameworks/api

Typed client for the FrameWorks GraphQL API. It ships the public operation set as typed
documents, plus the parts every integration writes by hand otherwise: retries, pagination, typed errors, a server
version check, VOD uploads, playback token signing, and webhook verification.

The Go (`github.com/Livepeer-FrameWorks/sdk-go`) and Python (`livepeer-frameworks`) SDKs are generated from the same
operations and share this package's version.

```sh
npm install @livepeer-frameworks/api
# subscriptions only:
npm install graphql-ws
```

Node 22 or later, or any browser with `fetch`.

```ts
import { createClient, CreateStreamDocument, expectResult } from "@livepeer-frameworks/api";

const client = createClient({
  url: "https://bridge.example.com/graphql",
  token: process.env.FRAMEWORKS_API_TOKEN,
});

const data = await client.request(CreateStreamDocument, { input: { name: "Launch stream" } });
const stream = expectResult(data.createStream, "Stream");
console.log(stream.streamKey, stream.playbackId);
```

| Entry                                    | Contents                                                              |
| ---------------------------------------- | --------------------------------------------------------------------- |
| `@livepeer-frameworks/api`               | client, typed documents, errors, pagination, uploads, playback tokens |
| `@livepeer-frameworks/api/subscriptions` | WebSocket subscriptions (needs the `graphql-ws` peer dependency)      |
| `@livepeer-frameworks/api/webhooks`      | webhook signature verification and typed event parsing                |

## Versions

The SDK is 0.x until the platform reaches 1.0. Each minor line (0.1, 0.2, ...) supports every FrameWorks release from
its minimum server version on; a new minor line may drop operations or raise the minimum. The client checks the
server's `serverInfo`, shared per URL and read again after five minutes, and throws `ServerTooOldError` against an older
release.

Documentation: https://logbook.frameworks.network/builders/sdks
