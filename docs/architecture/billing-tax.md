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
