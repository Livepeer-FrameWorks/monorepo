# Mist trigger contract

FrameWorks installs eighteen MistServer triggers through Helmsman. Six are synchronous decisions; twelve are asynchronous events. Mist owns transport and applies an explicit outcome. Helmsman and Foghorn own routing, admission, tenant policy, local durability, and side effects.

This contract is specific to the FrameWorks Mist fork. Mist and Helmsman are deployed as a coordinated media-node change; there is no trigger protocol version or negotiation field.

## Synchronous outcomes

An HTTP handler returns an optional `X-Mist-Trigger-Action` header and an optional `X-Mist-Trigger-Reason`. The action is one of:

| Action           | Meaning                                                                                       |
| ---------------- | --------------------------------------------------------------------------------------------- |
| `value`          | Use the response body as the trigger value.                                                   |
| `deny`           | Reject the operation. The body is not used as a stream name, source, process list, or target. |
| `keep`           | Keep the value that existed immediately before this handler ran.                              |
| `offline`        | Report a terminal source-offline result. Valid only for `STREAM_SOURCE`.                      |
| `use-configured` | Use the configured source or process list. Valid for `STREAM_SOURCE` and `STREAM_PROCESS`.    |

A successful response without the header retains Mist's legacy body interpretation. `STREAM_SOURCE` also retains the existing `offline:<reason>` response-body form for executable handlers and older HTTP handlers.

A blank handler, connection failure, timeout, non-2xx response, unknown action, or action invalid for the trigger is a handler failure. Mist applies that trigger entry's configured `onfail` action. This is separate from an authoritative `200` + `deny`: the former says the handler could not decide; the latter is a business decision.

Mist keeps its existing HTTP attempt behavior and five-second response deadline. Helmsman's blocking deadline is four seconds and follows the incoming HTTP request context, leaving time to return a typed result within Mist's attempt. There is no per-trigger timeout or retry configuration.

Multiple matching handlers run in order. `keep` restores the value from immediately before that handler, and `deny` is terminal. Missing trigger configuration is different from a failed handler: Mist skips the trigger entirely. Helmsman's drift reconciler therefore compares and repairs the full definition of all eighteen managed entries, not only their fallback values.

## Blocking trigger matrix

| Trigger          | Purpose                                                                | Successful actions                           | `onfail`  | Important call-site behavior                                                                                                                                                                  |
| ---------------- | ---------------------------------------------------------------------- | -------------------------------------------- | --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PUSH_REWRITE`   | Authenticate and admit an ingest; rewrite to its runtime stream.       | `value`, `deny`, `keep`                      | `deny`    | `value` is sanitized and becomes the pushed stream name. No admission answer means no ingest.                                                                                                 |
| `PLAY_REWRITE`   | Resolve a public playback ID and apply resolve-time policy/accounting. | `value`, `deny`, `keep`                      | `deny`    | `deny` ends playback immediately. There is no unresolved sentinel. A literal `true` is a stream name, not a permission grant.                                                                 |
| `STREAM_SOURCE`  | Select a concrete source, use configured source, or report offline.    | `value`, `offline`, `use-configured`, `keep` | `offline` | `offline` sets Mist's stream-offline state and reports the reason; it is not attempted as a source URI.                                                                                       |
| `STREAM_PROCESS` | Supply per-instance process JSON or use configured processes.          | `value`, `use-configured`, `keep`            | `keep`    | Resolution remains one-shot. Helmsman preinstalls the dispatched processing job's JSON before activating `processing+<hash>`. Missing job policy is a startup error, not a polling condition. |
| `PUSH_OUT_START` | Record/enrich an outbound push start while preserving its target.      | `value`, `deny`, `keep`                      | `keep`    | A Helmsman failure does not replace the target with a fallback string or kill multistreaming.                                                                                                 |
| `USER_NEW`       | Authorize and register a viewer session.                               | `value`, `deny`                              | `deny`    | Mist consumes this as a boolean admission decision. A valid Mist output JWT bypasses `USER_NEW` by Mist design; FrameWorks does not currently configure that JWK path.                        |

`defaultStream` is downstream stream-boot fallback configuration. It is not trigger failure policy, and FrameWorks does not use it to resolve failed `PLAY_REWRITE` decisions.

## FrameWorks response path

Foghorn returns a typed `MistTriggerResponse` to Helmsman. `action` is authoritative; the legacy `abort` field remains a compatibility alias for `deny`. Internal failures return a non-2xx HTTP response from Helmsman so Mist applies `onfail`. Successful empty values are never guessed generically: Foghorn maps them by trigger to `deny`, `keep`, or `use-configured`.

Mist supplies `X-Trigger-UUID` and `X-Trigger-UnixMillis`. Helmsman captures them on the common trigger envelope. For blocking requests, Foghorn coalesces and replays a complete prior result by `(authenticated node ID, trigger type, trigger UUID)` for ten minutes. That prevents repeated viewer/admission/accounting side effects when Mist retries after a lost response while still returning the exact decision Mist needs. The replay cache is process-local; it is not an exactly-once guarantee across a Foghorn failover.

Helmsman's 30-second `PLAY_REWRITE` recovery cache covers a different failure: a recently approved mapping can be reused when the Foghorn control stream is briefly unavailable. Recovery hits are counted because they skip fresh resolve-time side effects. Authoritative denials are never inserted into that cache.

## Asynchronous triggers

Mist sends `sync:false` triggers and returns after writing the request body. It does not read the HTTP status or body, so a Helmsman `503` is diagnostic and cannot make Mist retry.

| Trigger                               | Role                             | FrameWorks delivery after acceptance                                  |
| ------------------------------------- | -------------------------------- | --------------------------------------------------------------------- |
| `PUSH_END`                            | Final outbound-push facts        | Durable Helmsman WAL                                                  |
| `PUSH_INPUT_CLOSE`                    | Final ingest-connector facts     | Durable Helmsman WAL                                                  |
| `USER_END`                            | Final viewer-session facts       | Durable Helmsman WAL                                                  |
| `STREAM_END`                          | Final stream-session facts       | Durable Helmsman WAL                                                  |
| `RECORDING_END`                       | Final recording facts            | Durable Helmsman WAL                                                  |
| `RECORDING_SEGMENT`                   | Final recording-segment facts    | Durable Helmsman WAL                                                  |
| `LIVEPEER_SEGMENT_COMPLETE`           | Livepeer processing usage        | Durable Helmsman WAL                                                  |
| `PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE` | AV processing usage              | Durable Helmsman WAL                                                  |
| `STREAM_BUFFER`                       | Stream health/state              | Best effort                                                           |
| `LIVE_TRACK_LIST`                     | Track inventory                  | Best effort                                                           |
| `THUMBNAIL_UPDATED`                   | Thumbnail update                 | Best effort                                                           |
| `PROCESS_EXIT`                        | Local processing-job exit signal | Best effort; missing listeners and full queues are logged and counted |

For the eight durable entries, durability begins only after Helmsman fsyncs the local WAL row. Foghorn acknowledges after Decklog's Kafka publish commits; Helmsman retains and replays the row until that acknowledgement. See [Trigger durability](trigger-durability.md).

## Tenant authority and outages

Control-plane unavailability is not a healthy billing verdict. Quartermaster obtains its decision from Purser's bounded `GetTenantAdmissionStatus` RPC, which reads subscription, balance, active reservations, and collection readiness without loading entitlements, allowance usage, retention, or storage pricing. Foghorn represents tenant billing state as `healthy`, `denied`, `stale-valid`, or `unavailable`, keyed by tenant rather than only by stream. A verified decision is fresh for ten minutes and may be served for five more minutes while a background refresh fails. A cold miss, status older than that grace window, or an explicit billing invalidation is unavailable rather than an unmetered allow. Post-payment verification always performs a fresh admission lookup.

The current billing-change fan-out carries only `{tenant_id, reason}`. It does not carry a replacement snapshot, version, validity bounds, or signature, so it cannot yet be treated as durable local authority. Building autonomous media-cluster authority requires a separately reviewed, versioned replacement/revocation path with convergence after missed notifications. Existing HMAC playback-policy bundles do not contain the required billing fields and are not silently repurposed here.

## Configuration limits

Mist stores trigger handler URLs and legacy default strings in 128-byte shared-memory fields, so configured values must fit within 127 bytes. The trigger shared-memory page is created with a 32 KiB capacity; readers map the existing object's actual size with `fstat`. The apparent 8 KiB constructor argument on the reader is therefore not a reader/writer mismatch and is not part of this change.

## Configuration ownership and deployment

Helmsman is the authority for the complete managed trigger set. It applies the set after receiving a Foghorn config seed, saves it through the Mist API, and repairs missing or drifted entries every 30 seconds.

The tracked `infrastructure/mistserver.conf` is only the dev compose runtime snapshot; production deployment does not copy it. Native Linux provisioning preserves `/etc/mistserver.conf` while seeding the controller bind settings. The production edge container seeds a minimal `/etc/frameworks/mistserver.conf` inside its persistent `frameworks_etc` volume. In both production shapes, the full stream and trigger configuration arrives from Helmsman's runtime reconciliation rather than an Ansible trigger template.
