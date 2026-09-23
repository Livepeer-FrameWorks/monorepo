# @livepeer-frameworks/api

Typed client for the FrameWorks GraphQL API. It ships a typed document for every public root field and argument-taking
field of the public schema, typed custom selections, and the parts every integration writes by hand otherwise: retries,
pagination, typed errors, a server version check, VOD uploads, playback token signing, and webhook verification.

The Go (`github.com/Livepeer-FrameWorks/sdk-go`) and Python (`livepeer-frameworks`) SDKs are generated from the same
schema and operations and share this package's version.

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
| `@livepeer-frameworks/api/select`        | typed custom selections over the whole public schema                  |
| `@livepeer-frameworks/api/webhooks`      | webhook signature verification and typed event parsing                |

## Custom selections

Every public root field and argument-taking field has a generated document with a default selection. For fields beyond
it, `@livepeer-frameworks/api/select` builds typed operations from selection objects (generated with
[Genql](https://genql.dev) from the public schema) and sends them through an existing client, so authentication,
retries, typed errors, and the server version check are the same as for `client.request`:

```ts
import { createClient } from "@livepeer-frameworks/api";
import { createSelectClient } from "@livepeer-frameworks/api/select";
import { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";

const select = createSelectClient(createClient({ url, token }), {
  subscriptions: createSubscriptionClient({ url: wsUrl, token }), // only for subscription()
});

const { stream } = await select.query({
  stream: { __args: { id }, name: true, metrics: { status: true, currentViewers: true } },
});

const { createStream } = await select.mutation({
  createStream: {
    __args: { input: { name: "Launch stream" } },
    __typename: true,
    on_Stream: { id: true, streamKey: true },
    on_ValidationError: { message: true, field: true },
  },
});
```

`__args` passes arguments, `on_<Type>` selects a union member or interface implementation, and `__scalar: true`
selects every scalar field of a level that needs no argument. `__scalar` is refused at the mutation and subscription
roots: name each root field the operation runs. A selected field the schema does not have throws `TypeError` before
anything is sent. Results are typed from the selection: a union or interface result is the union of its members, each
with `__typename` (always selected there, so the result can be narrowed) and only the fields its branches select.
`__name` names the operation, and may not reuse an SDK operation name. The entry carries the schema's type map (about 20 kB gzipped), which
the main entry does not include.

## Versions

The SDK is 0.x until the platform reaches 1.0. Each minor line (0.1, 0.2, ...) supports every FrameWorks release from
its minimum server version on; a new minor line may drop operations or raise the minimum. The client checks the
server's `serverInfo`, shared per URL and read again after five minutes, and throws `ServerTooOldError` against an older
release.

Documentation: https://logbook.frameworks.network/builders/sdks
