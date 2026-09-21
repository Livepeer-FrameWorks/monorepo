package handlers

import (
	"fmt"
	"html/template"

	"frameworks/api_billing/internal/appconfig"

	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
)

type billingEmailMeta struct {
	Preheader string
	Eyebrow   string
	Title     string
}

var billingEmailMetaByName = map[string]billingEmailMeta{
	"invoice_created": {
		Preheader: "A new FrameWorks invoice is ready to review.",
		Eyebrow:   "Billing",
		Title:     "Your new invoice is ready",
	},
	"payment_success": {
		Preheader: "Your FrameWorks payment was confirmed.",
		Eyebrow:   "Payment received",
		Title:     "Payment Confirmed",
	},
	"payment_failed": {
		Preheader: "We could not process your FrameWorks payment.",
		Eyebrow:   "Payment issue",
		Title:     "Payment failed",
	},
	"payment_action_required": {
		Preheader: "Your bank needs extra confirmation to complete this payment.",
		Eyebrow:   "Action required",
		Title:     "Confirm your payment",
	},
	"overdue_reminder": {
		Preheader: "A FrameWorks invoice is overdue.",
		Eyebrow:   "Payment reminder",
		Title:     "Your invoice is overdue",
	},
	"account_suspended": {
		Preheader: "Your FrameWorks account needs a balance top-up.",
		Eyebrow:   "Account notice",
		Title:     "Account suspended",
	},
}

var billingEmailTemplates = map[string]string{
	"invoice_created": `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{if .TenantName}}Hello {{.TenantName}},{{else}}Hello,{{end}}</p>
<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">A new invoice has been generated for your FrameWorks account.</p>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; margin:20px 0; border-collapse:collapse; background:#eef5f8; border-left:3px solid #0f4b6e;">
  <tr><td style="padding:12px 15px; color:#667085; font-size:13px; border-bottom:1px solid #ccdde5;">Invoice</td><td align="right" style="padding:12px 15px; color:#24283b; font-size:14px; font-weight:bold; border-bottom:1px solid #ccdde5;">{{.InvoiceID}}</td></tr>
  <tr><td style="padding:12px 15px; color:#667085; font-size:13px; border-bottom:1px solid #ccdde5;">Amount</td><td align="right" style="padding:12px 15px; color:#24283b; font-size:14px; font-weight:bold; border-bottom:1px solid #ccdde5;">{{money .Amount}} {{.Currency}}</td></tr>
  {{if .FX.UnitsPerEUR}}<tr><td style="padding:12px 15px; color:#667085; font-size:13px; border-bottom:1px solid #ccdde5;">EUR total</td><td align="right" style="padding:12px 15px; color:#24283b; font-size:14px; font-weight:bold; border-bottom:1px solid #ccdde5;">{{.FX.EURAmount}} EUR at {{.FX.UnitsPerEUR}} {{.Currency}} per EUR (ECB reference rate of {{.FX.ReferenceDate}})</td></tr>{{end}}
  <tr><td style="padding:12px 15px; color:#667085; font-size:13px;">Due date</td><td align="right" style="padding:12px 15px; color:#24283b; font-size:14px; font-weight:bold;">{{.DueDate.Format "January 2, 2006"}}</td></tr>
</table>
{{if .UsageWaived}}<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #2f7d69; background:#edf8f4; color:#295f52; font-size:14px; line-height:21px;"><strong>Usage is on us during beta.</strong><br>Metered usage would have cost {{money .GrossMeteredAmount}} EUR. Your metered total is 0.00 EUR.</div>{{end}}
{{if .LineItemGroups}}
<h2 style="margin:28px 0 12px; color:#24283b; font-size:18px; line-height:24px;">Charges</h2>
{{range .LineItemGroups}}
<h3 style="margin:20px 0 8px; color:#24283b; font-size:15px; line-height:21px;">{{if .ClusterName}}{{.ClusterName}}{{else}}Cluster {{.ClusterID}}{{end}}{{if eq .ClusterKind "tenant_private"}} <span style="color:#2f7d69; font-size:12px;">· Self-hosted</span>{{end}}{{if eq .ClusterKind "third_party_marketplace"}} <span style="color:#0f4b6e; font-size:12px;">· Marketplace</span>{{end}}{{if eq .ClusterKind "platform_official"}} <span style="color:#0f4b6e; font-size:12px;">· Platform</span>{{end}}</h3>
<table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin-bottom:14px; font-size:13px;">
  <tr style="background:#eef5f8;"><th align="left" style="padding:9px 8px; color:#3d4a68; border-bottom:1px solid #ccdde5;">Item</th><th align="right" style="padding:9px 8px; color:#3d4a68; border-bottom:1px solid #ccdde5;">Quantity</th><th align="right" style="padding:9px 8px; color:#3d4a68; border-bottom:1px solid #ccdde5;">Unit price</th><th align="right" style="padding:9px 8px; color:#3d4a68; border-bottom:1px solid #ccdde5;">Total</th></tr>
  {{range .Lines}}<tr>
    <td style="padding:9px 8px; color:#24283b; border-bottom:1px solid #e7edf0;">{{.Description}}{{if .DimensionLabel}}<div style="color:#667085; font-size:11px; line-height:16px;">{{.DimensionLabel}}</div>{{end}}{{if .PricingLabel}}<div style="color:#667085; font-size:11px; line-height:16px;">{{.PricingLabel}}</div>{{end}}</td>
    <td align="right" style="padding:9px 8px; color:#24283b; border-bottom:1px solid #e7edf0;">{{.Quantity}}{{if .Unit}} {{.Unit}}{{end}}</td>
    <td align="right" style="padding:9px 8px; color:#24283b; border-bottom:1px solid #e7edf0;">{{.UnitPrice}} {{.Currency}}</td>
    <td align="right" style="padding:9px 8px; color:#24283b; border-bottom:1px solid #e7edf0;">{{if and .IsZeroPrice (ne .PricingSource "beta_free")}}<span style="color:#2f7d69; font-weight:bold;">Included</span>{{else}}{{.Total}} {{.Currency}}{{end}}</td>
  </tr>{{end}}
</table>
{{end}}{{end}}
<p style="margin:20px 0 0; color:#24283b; font-size:15px; line-height:23px;">{{if .PaymentRequired}}Open the invoice to choose a payment method and pay the outstanding balance.{{else}}No payment is required. You can still review the invoice details.{{end}}</p>
{{template "action" (action .LoginURL (ifText .PaymentRequired "View and pay invoice" "View invoice"))}}`,

	"payment_success": `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{if .TenantName}}Hello {{.TenantName}},{{else}}Hello,{{end}}</p>
<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">We've received your payment. Thank you.</p>
<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #2f7d69; background:#edf8f4; color:#295f52; font-size:14px; line-height:22px;"><strong>Invoice {{.InvoiceID}}</strong><br>Amount paid: {{money .Amount}} {{.Currency}}{{if .FX.UnitsPerEUR}}<br>EUR applied: {{.FX.EURAmount}} EUR at {{.FX.UnitsPerEUR}} {{.Currency}} per EUR (ECB reference rate of {{.FX.ReferenceDate}}){{end}}<br>Payment method: {{.PaymentMethod}}{{if .PaidAt}}<br>Payment date: {{.PaidAt.Format "January 2, 2006 at 3:04 PM"}}{{end}}</div>
<p style="margin:0; color:#24283b; font-size:15px; line-height:23px;">Your account is up to date and services remain active.</p>
{{template "action" (action .LoginURL "View billing")}}`,

	"payment_failed": `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{if .TenantName}}Hello {{.TenantName}},{{else}}Hello,{{end}}</p>
<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">We were unable to process your payment.</p>
<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #b74242; background:#fff1f1; color:#7c3030; font-size:14px; line-height:22px;"><strong>Invoice {{.InvoiceID}}</strong><br>Amount: {{money .Amount}} {{.Currency}}{{if .FX.UnitsPerEUR}}<br>EUR amount: {{.FX.EURAmount}} EUR at {{.FX.UnitsPerEUR}} {{.Currency}} per EUR (ECB reference rate of {{.FX.ReferenceDate}}){{end}}<br>Payment method: {{.PaymentMethod}}</div>
<p style="margin:0; color:#24283b; font-size:15px; line-height:23px;">Check your payment method and try again. Contact your bank if the issue continues.</p>
{{template "action" (action .LoginURL "Retry payment")}}`,

	"payment_action_required": `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{if .TenantName}}Hello {{.TenantName}},{{else}}Hello,{{end}}</p>
<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">Your bank requires extra confirmation before we can complete this payment.</p>
<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #a66b16; background:#fff8e8; color:#6f4a16; font-size:14px; line-height:22px;"><strong>Invoice {{.InvoiceID}}</strong><br>Amount: {{money .Amount}} {{.Currency}}{{if .FX.UnitsPerEUR}}<br>EUR amount: {{.FX.EURAmount}} EUR at {{.FX.UnitsPerEUR}} {{.Currency}} per EUR (ECB reference rate of {{.FX.ReferenceDate}}){{end}}</div>
{{template "action" (action .ActionURL "Confirm payment")}}
<p style="margin:0; color:#667085; font-size:13px; line-height:20px;">If you did not initiate this payment, contact the FrameWorks support team.</p>`,

	"overdue_reminder": `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{if .TenantName}}Hello {{.TenantName}},{{else}}Hello,{{end}}</p>
<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">This is a reminder that the following invoice is overdue.</p>
<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #a66b16; background:#fff8e8; color:#6f4a16; font-size:14px; line-height:22px;"><strong>Invoice {{.InvoiceID}}</strong><br>Amount due: {{money .Amount}} {{.Currency}}{{if .FX.UnitsPerEUR}}<br>Invoice EUR total: {{.FX.EURAmount}} EUR at {{.FX.UnitsPerEUR}} {{.Currency}} per EUR (ECB reference rate of {{.FX.ReferenceDate}}){{end}}<br>Overdue by {{.DaysPastDue}} days</div>
<p style="margin:0; color:#24283b; font-size:15px; line-height:23px;">Please make payment to avoid service interruption.</p>
{{template "action" (action .LoginURL "Pay now")}}`,

	"account_suspended": `
<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">{{if .TenantName}}Hello {{.TenantName}},{{else}}Hello,{{end}}</p>
<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">Your account has been suspended because its prepaid balance is negative.</p>
<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #b74242; background:#fff1f1; color:#7c3030; font-size:14px; line-height:22px;"><strong>Current balance</strong><br>{{money .Balance}} {{.Currency}}</div>
<p style="margin:0; color:#24283b; font-size:15px; line-height:23px;">Top up the balance to restore access and continue creating resources.</p>
{{template "action" (action .LoginURL "Go to billing")}}
<p style="margin:0; color:#667085; font-size:13px; line-height:20px;">If you believe this is a mistake, contact the FrameWorks support team.</p>`,
}

func (es *EmailService) renderBillingTemplate(templateName string, data EmailData) (string, error) {
	contentTemplate, exists := billingEmailTemplates[templateName]
	if !exists {
		return "", fmt.Errorf("template %s not found", templateName)
	}
	meta := billingEmailMetaByName[templateName]
	funcs := template.FuncMap{
		"action": func(actionURL, label string) emailpkg.Action {
			return emailpkg.Action{URL: actionURL, Label: label}
		},
		"ifText": func(condition bool, whenTrue, whenFalse string) string {
			if condition {
				return whenTrue
			}
			return whenFalse
		},
		"money": func(amount float64) string {
			return fmt.Sprintf("%.2f", amount)
		},
	}
	rt := appconfig.Runtime()
	return emailpkg.RenderLayout(emailpkg.LayoutData{
		LogoURL:      rt.Logo(),
		Preheader:    meta.Preheader,
		Eyebrow:      meta.Eyebrow,
		Title:        meta.Title,
		SupportEmail: rt.Support(),
		Content:      data,
	}, contentTemplate, funcs)
}
