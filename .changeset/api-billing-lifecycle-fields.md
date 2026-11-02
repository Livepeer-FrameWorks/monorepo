---
"@livepeer-frameworks/api": minor
---

New `GetBillingLifecycle` operation (`billingLifecycle` query, served from v0.3.14) returns the plan lifecycle: `effectiveBasePrice`, `operatorGrant` (the terms of a FrameWorks arrangement such as a trial, when it ends and the plan it returns to), `freeFallback` (a scheduled move to Free after a failed payment or a cancellation, with its date and stage), the pending plan change with `pendingReason` (`PendingTierReason`) and `pendingTierState` (whether a scheduled downgrade can still be cancelled), `downgradeEffectiveAt` (when a downgrade chosen now takes effect) and `freeMoveFinalAt` (until when a move to Free chosen now can be undone). `changeBillingTier` and the checkout mutations return typed refusal codes such as `OUTSTANDING_GRANT`, `PENDING_CHECKOUT` and `PAID_TERM_ENDING` in `ValidationError.code`.
