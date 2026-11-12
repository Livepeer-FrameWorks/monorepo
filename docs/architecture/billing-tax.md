# Billing tax and financial evidence

Purser issues documents on the configured supplier's behalf. For the hosted
service, DDVTech B.V. is that supplier: these are DDVTech's sales records, rather
than invoices from Purser to DDVTech. Independently operated installations retain
their own supplier identity.

## Shared assessment

`api_billing/internal/tax.Assess` owns the Dutch supplier service policy. Each
assessment fixes its purpose and tax point before loading rates or validating a
claim. Prices exclude VAT.

| Saved customer profile                                   | Treatment                                                 |
| -------------------------------------------------------- | --------------------------------------------------------- |
| Netherlands, personal or business                        | Effective Dutch VAT, currently 21%, added to the price    |
| Consumer elsewhere in the EU                             | Effective Dutch VAT added to the price                    |
| Business elsewhere in the EU with validated VAT evidence | Reverse charge, with VAT identifiers and required wording |
| Customer outside the EU                                  | Outside the scope of Dutch VAT                            |

Customers save their location and select Personal or Business; they cannot
select an exemption. Shared country/VAT normalization includes Greece's GR
address code and EL VAT prefix. A company name alone does not establish business
status. This service policy does not infer foreign tax registrations.

Assessment purpose determines handling of an incomplete claim:

- **Checkout:** obtain required details and validate the claim before accepting
  a new purchase. Unavailable verification remains retryable.
- **Recurring:** incomplete details or a new/changed unproven EU VAT claim use
  positive Dutch VAT and a visible review. Database failures retry without
  changing collection.
- **Invoice:** ordinary issuance requires a complete profile and supported
  assessment. Transient failures roll back for normal retry.
- **Historical:** use accepted evidence at the original tax point; never
  reassess today's profile to rewrite an issued document.

Recurring and invoice assessment apply to relationships with assessed tax
handling; see [Relationships that predate assessed tax](#relationships-that-predate-assessed-tax).

The Billing page shows incomplete/review-required recurring profiles. Each
fallback invoice has its own operator review; acknowledging an older document
does not hide later ones. Correcting details affects future collections.

When a previously established reverse-charge setup is unchanged, a transient
VIES outage does not replace its provider terms with Dutch VAT. Reconciliation
checks the full normalized legal identity, VAT claim, supplier and retained
recurring terms, leaves the actual collection settings unchanged and retries.
A matching accepted checkout profile can establish this continuity before the
first recurring setup. This does not turn current validation into historical
proof or repair an issued document.

## Relationships that predate assessed tax

A billing relationship whose `tenant_subscriptions` row existed before
assessed tax handling has no `tax_assessed_since` and keeps the treatment it
was sold under:

- recurring tax reconciliation does not touch its Stripe or Mollie customer,
  tax IDs, subscription tax rates or collection amount, and stores no provider
  tax profile, expectation or review;
- its recurring Purser invoices (postpaid usage and advance base fees) are
  collected for the net amount with no VAT line. They keep
  `tax_status = 'legacy_unknown'` and are never held for a tax review.
- the Stripe invoices it is issued, and their credit notes, carry no Purser
  tax profile. Purser imports them as `legacy_unknown` without a document
  review. The decision uses the invoice's issue time, so an invoice issued
  before `tax_assessed_since` keeps this treatment after the move; one issued
  later without a tax profile is a synchronization failure and is reviewed.

The relationship moves to assessed handling, and records `tax_assessed_since`,
when the tenant saves a complete billing profile (name, email, street, city,
postal code and a valid country), starts a Stripe or Mollie checkout, or upgrades
its plan. Each of those paths already requires a complete saved profile. From then on
its provider subscriptions and invoices follow the assessment rules above. A
relationship created afterwards starts with assessed handling. One-off purchases
such as prepaid top-ups are always assessed.

## Documents, money and providers

Purser freezes supplier/customer snapshots, tax point, policy, net/VAT/gross and
accepted evidence. PDFs and emails render these facts. Credit notes carry the
original supply's tax. Existing paid customers are not charged again to recover
historical missing VAT.

Stripe remains subscription collector and original issuer for its supplies.
Purser synchronizes exclusive tax rates, imports the original invoice or credit
note, and retains its provider number and PDF. It does not duplicate the same
subscription supply on a Purser invoice. Payment retries retain their accepted
terms and request identity.

Mollie native payments produce Purser invoices from accepted collection evidence.
Confirmed money remains recorded if its document needs review. Missing legal
proof does not justify inventing a supplier or net/VAT split; payment lifecycle
updates remain independent of that review.

New prepaid purchases collect gross cash and grant net credit: EUR 100 net at
21% VAT collects EUR 121 and adds EUR 100 spendable credit. Payment, funding lot
and tax document are recorded together. Consumption carries the original tax so
VAT is not charged twice. Refunds reverse the original tax and refunded net
credit. Legacy balances with unknown tax stay identified as such.

## Evidence and operator review

VIES responses are retained with assessments. A positive result can bridge a
transient outage for seven days after its normal 24-hour cache expiry. A newer
negative response or permanent SOAP fault prevents that fallback. Its use creates
revalidation work and a document-specific review, without rewriting issued facts.

Provider imports check accepted proof against issued parties, money and tax
point. Original raw facts, amounts, number and PDF remain retained. Redelivery
does not reassess an already-retained unknown document against today's profile.

Operators inspect reviews and acknowledge an exact evidence hash with a reason.
Acknowledgment records a decision; it does not validate a payment, repair an
invoice, change tax totals or dismiss scanner/funds failures. The same facts do
not reopen an acknowledged review; changed evidence can reopen it.

## Accounting boundary

`frameworks admin billing finance audit` exports observed evidence with hashes
and actionable exceptions. It does not produce balanced journal entries,
reconcile a payout to the bank, approve withdrawals or certify an accounting
close. See the [operator guide](../../website_docs/src/content/docs/operators/billing-financials.mdx)
and the remaining [close/journal proposal](../rfcs/RFC_billing_documents_operator_financials.md).

## Native provider settlements and EUR reference valuation

`purser.provider_settlements` preserves the provider balance transaction's native
currency, gross, fee, net, transaction ID, timestamp and charge-to-settlement
exchange rate when conversion occurs. Stripe supplies that rate; for Mollie the
existing effective ratio is derived from native gross and the recorded charge
amount. Native evidence is committed before EUR valuation. A same-currency
provider settlement needs no provider exchange rate, regardless of the ledger
currency.

The separate EUR fields are a reference valuation using the existing ECB rate
for the transaction's UTC date (maximum reference age five days). They do not
assert that the provider converted cash into EUR. Gross and fee are rounded once;
EUR net is their difference. EUR settlements use an identity valuation. A
compatibility trigger supplies that complete identity record for older EUR
writers during rollout.

States distinguish `pending` provider evidence, `recorded` native evidence awaiting
valuation, `blocked` unsupported valuation policy, and `settled` complete evidence
and valuation. Missing reference rates retain native facts and retry valuation
without calling the provider again. Unsupported currency stops provider polling
and stays in operator recovery inventory. Adding support lets the ordinary
valuation job resume from the same facts. Valuation and waking a bound blocked
callback commit together; neither duplicates payment, customer credit, invoice
activation or provider charges.

Verified callbacks bind to a tenant through the recorded provider payment. Stripe
charge IDs are never used in place of PaymentIntent IDs. Request tenant headers
cannot establish ownership. Internal reconciliation preserves typed waiting,
blocked and transient causes; the public failure response stays generic. Recovery
errors contain a fixed cause code and the verified payment ID, without embedding
provider payloads or signatures. Purser's alert count and the SSH-based deep
cluster doctor both read the service-owned `purser.billing_recovery_failures` view,
including poll-discovered settlements with no callback. A settlement valuation
and its callback are distinct retained obligations when both are blocked,
correlated by payment ID. Blocked records remain visible and normal settlement waits become actionable
only after the existing age threshold.

The balance-side amount semantics follow [Mollie's balance transaction contract](https://docs.mollie.com/reference/list-balance-transactions).
