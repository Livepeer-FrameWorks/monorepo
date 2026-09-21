// Sign a viewer token for content with a JWT playback policy, and resolve
// the viewer endpoint with it.
import {
  createClient,
  ResolveViewerEndpointDocument,
  signPlaybackToken,
} from "@livepeer-frameworks/api";

const token = await signPlaybackToken({
  privateKeyPem: process.env.FRAMEWORKS_SIGNING_KEY_PEM ?? "",
  kid: process.env.FRAMEWORKS_SIGNING_KID ?? "",
  expiresIn: 300,
  subject: "viewer-42",
  audience: "viewer",
  claims: { tier: "pro" },
});

const client = createClient({ url: "https://bridge.example.com/graphql" });
const data = await client.request(
  ResolveViewerEndpointDocument,
  { contentId: "k3v9x2m7q4w8", protocol: "HLS" },
  { playbackToken: token }
);
// The media URL itself also needs the token: append ?jwt=<token>.
console.log(`${data.resolveViewerEndpoint?.primary?.url}?jwt=${token}`);
