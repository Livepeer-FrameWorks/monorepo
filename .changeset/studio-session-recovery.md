---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

A failed connection now returns to `capturing` when sources are still live, with the reason in the state context, so a broadcaster can go live again without re-adding sources. The state stays `error` when there is nothing to publish. The advanced panel masks the stream key in the WHIP URL it displays; copying still gives the full URL.
