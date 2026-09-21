package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"frameworks/api_billing/internal/appconfig"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/shopspring/decimal"
)

// EmailService handles email notifications
type EmailService struct {
	smtpHost      string
	smtpPort      int
	smtpUser      string
	smtpPassword  string
	fromEmail     string
	fromName      string
	allowInsecure bool
	logger        logging.Logger
}

// EmailData represents data for email templates
type EmailData struct {
	TenantName      string
	InvoiceID       string
	Amount          float64
	Currency        string
	DueDate         time.Time
	PaidAt          *time.Time
	PaymentMethod   string
	DaysPastDue     int
	Balance         float64
	LoginURL        string
	PaymentRequired bool
	// ActionURL is the hosted or in-app page where the customer completes a
	// required payment action.
	ActionURL string
	// UsageWaived is true when metered usage was waived to €0 during the
	// billing beta while the subscription still charges. GrossMeteredAmount is
	// the would-have-cost usage total shown in the waiver callout.
	UsageWaived        bool
	GrossMeteredAmount float64
	// LineItems is the cluster-attributed presentation source of truth for
	// the invoice. Email renders only from this — usage_details is raw/debug
	// JSON kept for audit, never read here.
	LineItems []EmailInvoiceLineItem
	// LineItemGroups is the same data grouped by cluster for the template
	// loop. Built by buildEmailInvoiceData; the template is otherwise
	// responsible for nothing structural.
	LineItemGroups []EmailLineItemGroup
	// FX states the EUR equivalent of Amount when Currency is not EUR.
	FX EmailFX
}

// EmailFX is the EUR amount a non-EUR amount converts to at an ECB reference
// rate. The zero value renders nothing, which is the case for EUR amounts.
type EmailFX struct {
	EURAmount     string
	UnitsPerEUR   string
	ReferenceDate string
}

// NewEmailFX returns the FX statement for an amount charged in currency. EUR
// amounts and amounts without a recorded rate return the zero value.
func NewEmailFX(currency string, eurCents int64, unitsPerEUR, referenceDate string) EmailFX {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	unitsPerEUR = strings.TrimSpace(unitsPerEUR)
	if currency == "" || currency == "EUR" || unitsPerEUR == "" || referenceDate == "" {
		return EmailFX{}
	}
	if rate, err := decimal.NewFromString(unitsPerEUR); err == nil {
		unitsPerEUR = rate.String()
	}
	return EmailFX{
		EURAmount:     decimal.New(eurCents, -2).StringFixed(2),
		UnitsPerEUR:   unitsPerEUR,
		ReferenceDate: referenceDate,
	}
}

// EmailInvoiceLineItem is a local presentation DTO for email templates. Built
// from persisted purser.invoice_line_items. Decoupled from rating.LineItem so
// that template-shape changes don't ripple into the rating engine.
type EmailInvoiceLineItem struct {
	Description    string
	Unit           string
	DimensionLabel string
	ClusterID      string
	ClusterName    string
	ClusterKind    string
	Quantity       string
	UnitPrice      string
	Total          string
	Currency       string
	PricingSource  string
	PricingLabel   string
	IsZeroPrice    bool
}

// EmailLineItemGroup is one cluster (or tenant-scope bucket) in the
// rendered table. PlatformScoped is true for tenant-level lines
// (base_subscription); ClusterID/ClusterName are empty in that case.
type EmailLineItemGroup struct {
	ClusterID      string
	ClusterName    string
	ClusterKind    string
	PlatformScoped bool
	Lines          []EmailInvoiceLineItem
}

// NewEmailService creates a new email service instance
func NewEmailService(logger logging.Logger) *EmailService {
	rt := appconfig.Runtime()
	port := rt.SMTPPort
	if port == 0 {
		port = 587 // Default SMTP port
	}

	fromName := strings.TrimSpace(rt.FromName)
	if fromName == "" {
		fromName = "FrameWorks"
	}
	return &EmailService{
		smtpHost:      rt.SMTPHost,
		smtpPort:      port,
		smtpUser:      rt.SMTPUser,
		smtpPassword:  rt.SMTPPassword,
		fromEmail:     rt.FromEmail,
		fromName:      fromName,
		allowInsecure: rt.SMTPAllowInsecure,
		logger:        logger,
	}
}

// IsConfigured checks if email service is properly configured
func (es *EmailService) IsConfigured() bool {
	return es.smtpHost != "" && es.smtpUser != "" && es.smtpPassword != "" && es.fromEmail != ""
}

// SendInvoiceCreatedEmail sends notification when a new invoice is created.
// amount and currency are the total charged; fx states the EUR invoice total
// when that currency is not EUR. meteredAmount and grossMeteredAmount are EUR.
// lineItems is the cluster-attributed presentation source of truth — the
// caller queries purser.invoice_line_items and maps to []EmailInvoiceLineItem
// before invoking. Do not pass usage_details; that JSON is raw/debug only.
func (es *EmailService) SendInvoiceCreatedEmail(tenantEmail, tenantName, invoiceID string, amount, meteredAmount, grossMeteredAmount float64, currency string, dueDate time.Time, lineItems []EmailInvoiceLineItem, fx EmailFX) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping invoice created email")
		return nil
	}

	subject := fmt.Sprintf("New Invoice %s - FrameWorks", invoiceID)

	data := EmailData{
		TenantName:         tenantName,
		InvoiceID:          invoiceID,
		Amount:             amount,
		Currency:           currency,
		DueDate:            dueDate,
		LoginURL:           invoiceBillingURL(invoiceID),
		PaymentRequired:    amount > 0,
		LineItems:          lineItems,
		LineItemGroups:     groupEmailLineItems(lineItems),
		UsageWaived:        grossMeteredAmount > 0 && meteredAmount == 0,
		GrossMeteredAmount: grossMeteredAmount,
		FX:                 fx,
	}

	body, err := es.renderTemplate("invoice_created", data)
	if err != nil {
		return fmt.Errorf("failed to render invoice created template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// groupEmailLineItems splits line items into render-friendly groups: one
// group per cluster, plus a "Subscription" bucket for tenant-scoped lines
// (base_subscription). Groups are ordered: platform clusters first,
// tenant-private next, marketplace last; the subscription group is always
// last. Empty clusters are skipped.
func groupEmailLineItems(lines []EmailInvoiceLineItem) []EmailLineItemGroup {
	if len(lines) == 0 {
		return nil
	}
	platformScoped := []EmailInvoiceLineItem{}
	clusterGroups := map[string]*EmailLineItemGroup{}
	clusterOrder := []string{}
	for _, l := range lines {
		if l.ClusterID == "" {
			platformScoped = append(platformScoped, l)
			continue
		}
		grp, ok := clusterGroups[l.ClusterID]
		if !ok {
			grp = &EmailLineItemGroup{
				ClusterID:   l.ClusterID,
				ClusterName: l.ClusterName,
				ClusterKind: l.ClusterKind,
			}
			clusterGroups[l.ClusterID] = grp
			clusterOrder = append(clusterOrder, l.ClusterID)
		}
		grp.Lines = append(grp.Lines, l)
	}

	out := make([]EmailLineItemGroup, 0, len(clusterGroups)+1)
	// Platform-official → tenant_private → third_party_marketplace.
	for _, kind := range []string{"platform_official", "tenant_private", "third_party_marketplace"} {
		for _, cid := range clusterOrder {
			if clusterGroups[cid].ClusterKind == kind {
				out = append(out, *clusterGroups[cid])
			}
		}
	}
	// Then any cluster lines whose kind didn't match the canonical set.
	for _, cid := range clusterOrder {
		kind := clusterGroups[cid].ClusterKind
		if kind != "platform_official" && kind != "tenant_private" && kind != "third_party_marketplace" {
			out = append(out, *clusterGroups[cid])
		}
	}
	if len(platformScoped) > 0 {
		out = append(out, EmailLineItemGroup{
			ClusterName:    "Subscription",
			PlatformScoped: true,
			Lines:          platformScoped,
		})
	}
	return out
}

// SendPaymentSuccessEmail sends notification when payment is successful
func (es *EmailService) SendPaymentSuccessEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency, paymentMethod string, fx EmailFX) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping payment success email")
		return nil
	}

	subject := fmt.Sprintf("Payment Confirmed - Invoice %s", invoiceID)
	now := time.Now()

	data := EmailData{
		TenantName:    tenantName,
		InvoiceID:     invoiceID,
		Amount:        amount,
		Currency:      currency,
		PaidAt:        &now,
		PaymentMethod: paymentMethod,
		LoginURL:      invoiceBillingURL(invoiceID),
		FX:            fx,
	}

	body, err := es.renderTemplate("payment_success", data)
	if err != nil {
		return fmt.Errorf("failed to render payment success template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// SendPaymentFailedEmail sends notification when payment fails
func (es *EmailService) SendPaymentFailedEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency, paymentMethod string, fx EmailFX) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping payment failed email")
		return nil
	}

	subject := fmt.Sprintf("Payment Failed - Invoice %s", invoiceID)

	data := EmailData{
		TenantName:      tenantName,
		InvoiceID:       invoiceID,
		Amount:          amount,
		Currency:        currency,
		PaymentMethod:   paymentMethod,
		LoginURL:        invoiceBillingURL(invoiceID),
		PaymentRequired: amount > 0,
		FX:              fx,
	}

	body, err := es.renderTemplate("payment_failed", data)
	if err != nil {
		return fmt.Errorf("failed to render payment failed template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// SendPaymentActionRequiredEmail notifies the customer that a payment needs
// their authentication and links the relevant hosted or in-app resolution page.
func (es *EmailService) SendPaymentActionRequiredEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency, actionURL string, fx EmailFX) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping payment action-required email")
		return nil
	}

	subject := fmt.Sprintf("Action Required - Confirm Payment for Invoice %s", invoiceID)

	if actionURL == "" {
		actionURL = appconfig.Runtime().WebAppURL + "/login"
	}
	data := EmailData{
		TenantName: tenantName,
		InvoiceID:  invoiceID,
		Amount:     amount,
		Currency:   currency,
		ActionURL:  actionURL,
		FX:         fx,
	}

	body, err := es.renderTemplate("payment_action_required", data)
	if err != nil {
		return fmt.Errorf("failed to render payment action-required template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// SendOverdueReminderEmail sends reminder for overdue invoices. fx states the
// EUR invoice total when currency is not EUR.
func (es *EmailService) SendOverdueReminderEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency string, daysPastDue int, fx EmailFX) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping overdue reminder email")
		return nil
	}

	subject := fmt.Sprintf("Payment Reminder - Invoice %s (%d days overdue)", invoiceID, daysPastDue)

	data := EmailData{
		TenantName:      tenantName,
		InvoiceID:       invoiceID,
		Amount:          amount,
		Currency:        currency,
		DaysPastDue:     daysPastDue,
		LoginURL:        invoiceBillingURL(invoiceID),
		PaymentRequired: amount > 0,
		FX:              fx,
	}

	body, err := es.renderTemplate("overdue_reminder", data)
	if err != nil {
		return fmt.Errorf("failed to render overdue reminder template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

func invoiceBillingURL(invoiceID string) string {
	base := strings.TrimRight(appconfig.Runtime().WebAppURL, "/")
	return base + "/account/billing?invoice=" + url.QueryEscape(invoiceID)
}

// SendAccountSuspendedEmail sends notification when a tenant is suspended for negative balance
func (es *EmailService) SendAccountSuspendedEmail(tenantEmail, tenantName string, balance float64, currency string) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping account suspended email")
		return nil
	}

	subject := "Account Suspended - Negative Balance"

	data := EmailData{
		TenantName: tenantName,
		Balance:    balance,
		Currency:   currency,
		LoginURL:   strings.TrimRight(strings.TrimSpace(appconfig.Runtime().WebAppURL), "/") + "/account/billing",
	}

	body, err := es.renderTemplate("account_suspended", data)
	if err != nil {
		return fmt.Errorf("failed to render account suspended template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// sendEmail sends an email via SMTP
func (es *EmailService) sendEmail(to, subject, body string) error {
	sender := emailpkg.NewSender(emailpkg.Config{
		Host:          es.smtpHost,
		Port:          strconv.Itoa(es.smtpPort),
		User:          es.smtpUser,
		Password:      es.smtpPassword,
		From:          es.fromEmail,
		FromName:      es.fromName,
		AllowInsecure: es.allowInsecure,
	})
	err := sender.Send(context.Background(), emailpkg.Message{
		To:       to,
		Subject:  subject,
		HTMLBody: body,
		ReplyTo:  appconfig.Runtime().Support(),
	})

	if err != nil {
		es.logger.WithFields(logging.Fields{
			"error":   err.Error(),
			"to":      to,
			"subject": subject,
		}).Error("Failed to send email")
		return err
	}

	es.logger.WithFields(logging.Fields{
		"to":      to,
		"subject": subject,
	}).Info("Email sent successfully")

	return nil
}

// renderTemplate renders an email template with data.
func (es *EmailService) renderTemplate(templateName string, data EmailData) (string, error) {
	return es.renderBillingTemplate(templateName, data)
}
