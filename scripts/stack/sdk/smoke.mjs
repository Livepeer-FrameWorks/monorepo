// SDK smoke for @livepeer-frameworks/api (built dist): list, a tenantEvents
// subscription with a bearer token and no Origin, createStream observed on the
// subscription, deleteStream. Prints PASS/FAIL lines; exits 1 on any failure.
//   node smoke.mjs <repo> <graphql url> <ws url> <token>
const [repo, url, wsUrl, token] = process.argv.slice(2);
const sdk = await import(`${repo}/npm_api/dist/index.js`);
const subs = await import(`${repo}/npm_api/dist/subscriptions.js`);
let failed = 0;
const pass = (m) => console.log(`PASS  sdk-ts: ${m}`);
const fail = (m) => {
  failed++;
  console.log(`FAIL  sdk-ts: ${m}`);
};

const client = sdk.createClient({ url, token });
try {
  const list = await client.request(sdk.ListStreamsDocument, { page: { first: 5 } });
  typeof list.streamsConnection.totalCount === "number"
    ? pass("listStreams")
    : fail("listStreams shape");
} catch (e) {
  fail(`listStreams: ${e.message}`);
}

const sc = subs.createSubscriptionClient({ url: wsUrl, token });
const name = `stack-sdk-ts-${Date.now()}`;
let createdId = "";
const seen = new Promise((resolve) => {
  (async () => {
    try {
      for await (const ev of sc.subscribe(sdk.TenantEventsDocument, {
        types: ["stream.created"],
      })) {
        if (JSON.stringify(ev).includes(name)) return resolve(true);
      }
    } catch (e) {
      console.log(`  subscription ended: ${e.message}`);
    }
    resolve(false);
  })();
});
await new Promise((r) => setTimeout(r, 1500));
try {
  const created = await client.request(sdk.CreateStreamDocument, { input: { name } });
  createdId = created.createStream?.id ?? "";
  createdId ? pass("createStream") : fail(`createStream: ${JSON.stringify(created)}`);
} catch (e) {
  fail(`createStream: ${e.message}`);
}
const got = await Promise.race([seen, new Promise((r) => setTimeout(() => r(false), 20000))]);
got
  ? pass("tenantEvents delivered stream.created (bearer, no Origin)")
  : fail("tenantEvents did not deliver stream.created within 20 s");
await sc.close();
if (createdId) {
  try {
    const del = await client.request(sdk.DeleteStreamDocument, { id: createdId });
    del.deleteStream?.__typename === "DeleteSuccess"
      ? pass("deleteStream")
      : fail(`deleteStream: ${JSON.stringify(del)}`);
  } catch (e) {
    fail(`deleteStream: ${e.message}`);
  }
}
process.exit(failed ? 1 : 0);
