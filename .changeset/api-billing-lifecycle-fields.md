---
"@livepeer-frameworks/api": minor
---

`GetBillingStatus` returns the plan lifecycle: `effectiveBasePrice`, `operatorGrant` (the terms of a FrameWorks arrangement such as a trial, and when it ends), `freeFallback` (a scheduled move to Free after a failed payment or a cancellation, with its date and stage) and `pendingTierState` (whether a scheduled downgrade can still be cancelled). `changeBillingTier` and the checkout mutations return typed refusal codes such as `OUTSTANDING_GRANT`, `PENDING_CHECKOUT` and `PAID_TERM_ENDING` in `ValidationError.code`. `GetBillingTiers`, `GetBillingStatus` and `BillingTierFields` no longer select `slaLevel` or `features.sla`: self-serve plans carry no SLA.
