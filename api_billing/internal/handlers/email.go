package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"net/smtp"
	"os"
	"strconv"
	"time"

	"frameworks/pkg/logging"
)

// EmailService handles email notifications
type EmailService struct {
	smtpHost     string
	smtpPort     int
	smtpUser     string
	smtpPassword string
	fromEmail    string
	fromName     string
	logger       logging.Logger
}

// EmailData represents data for email templates
type EmailData struct {
	TenantName    string
	InvoiceID     string
	Amount        float64
	Currency      string
	DueDate       time.Time
	PaidAt        *time.Time
	PaymentMethod string
	DaysPastDue   int
	Balance       float64
	LoginURL      string
	// LineItems is the cluster-attributed presentation source of truth for
	// the invoice. Email renders only from this — usage_details is raw/debug
	// JSON kept for audit, never read here.
	LineItems []EmailInvoiceLineItem
	// LineItemGroups is the same data grouped by cluster for the template
	// loop. Built by buildEmailInvoiceData; the template is otherwise
	// responsible for nothing structural.
	LineItemGroups []EmailLineItemGroup
}

// EmailInvoiceLineItem is a local presentation DTO for email templates. Built
// from persisted purser.invoice_line_items. Decoupled from rating.LineItem so
// that template-shape changes don't ripple into the rating engine.
type EmailInvoiceLineItem struct {
	Description   string
	ClusterID     string
	ClusterName   string
	ClusterKind   string
	Quantity      string
	UnitPrice     string
	Total         string
	Currency      string
	PricingSource string
	PricingLabel  string
	IsZeroPrice   bool
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
	port, _ := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if port == 0 {
		port = 587 // Default SMTP port
	}

	return &EmailService{
		smtpHost:     os.Getenv("SMTP_HOST"),
		smtpPort:     port,
		smtpUser:     os.Getenv("SMTP_USER"),
		smtpPassword: os.Getenv("SMTP_PASSWORD"),
		fromEmail:    os.Getenv("FROM_EMAIL"),
		fromName:     os.Getenv("FROM_NAME"),
		logger:       logger,
	}
}

// IsConfigured checks if email service is properly configured
func (es *EmailService) IsConfigured() bool {
	return es.smtpHost != "" && es.smtpUser != "" && es.smtpPassword != "" && es.fromEmail != ""
}

// SendInvoiceCreatedEmail sends notification when a new invoice is created.
// lineItems is the cluster-attributed presentation source of truth — the
// caller queries purser.invoice_line_items and maps to []EmailInvoiceLineItem
// before invoking. Do not pass usage_details; that JSON is raw/debug only.
func (es *EmailService) SendInvoiceCreatedEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency string, dueDate time.Time, lineItems []EmailInvoiceLineItem) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping invoice created email")
		return nil
	}

	subject := fmt.Sprintf("New Invoice %s - FrameWorks", invoiceID)

	data := EmailData{
		TenantName:     tenantName,
		InvoiceID:      invoiceID,
		Amount:         amount,
		Currency:       currency,
		DueDate:        dueDate,
		LoginURL:       os.Getenv("WEBAPP_PUBLIC_URL") + "/login",
		LineItems:      lineItems,
		LineItemGroups: groupEmailLineItems(lineItems),
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
func (es *EmailService) SendPaymentSuccessEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency, paymentMethod string) error {
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
		LoginURL:      os.Getenv("WEBAPP_PUBLIC_URL") + "/login",
	}

	body, err := es.renderTemplate("payment_success", data)
	if err != nil {
		return fmt.Errorf("failed to render payment success template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// SendPaymentFailedEmail sends notification when payment fails
func (es *EmailService) SendPaymentFailedEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency, paymentMethod string) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping payment failed email")
		return nil
	}

	subject := fmt.Sprintf("Payment Failed - Invoice %s", invoiceID)

	data := EmailData{
		TenantName:    tenantName,
		InvoiceID:     invoiceID,
		Amount:        amount,
		Currency:      currency,
		PaymentMethod: paymentMethod,
		LoginURL:      os.Getenv("WEBAPP_PUBLIC_URL") + "/login",
	}

	body, err := es.renderTemplate("payment_failed", data)
	if err != nil {
		return fmt.Errorf("failed to render payment failed template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// SendOverdueReminderEmail sends reminder for overdue invoices
func (es *EmailService) SendOverdueReminderEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency string, daysPastDue int) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping overdue reminder email")
		return nil
	}

	subject := fmt.Sprintf("Payment Reminder - Invoice %s (%d days overdue)", invoiceID, daysPastDue)

	data := EmailData{
		TenantName:  tenantName,
		InvoiceID:   invoiceID,
		Amount:      amount,
		Currency:    currency,
		DaysPastDue: daysPastDue,
		LoginURL:    os.Getenv("WEBAPP_PUBLIC_URL") + "/login",
	}

	body, err := es.renderTemplate("overdue_reminder", data)
	if err != nil {
		return fmt.Errorf("failed to render overdue reminder template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
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
		LoginURL:   os.Getenv("WEBAPP_PUBLIC_URL") + "/account/billing",
	}

	body, err := es.renderTemplate("account_suspended", data)
	if err != nil {
		return fmt.Errorf("failed to render account suspended template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
}

// sendEmail sends an email via SMTP
func (es *EmailService) sendEmail(to, subject, body string) error {
	auth := smtp.PlainAuth("", es.smtpUser, es.smtpPassword, es.smtpHost)

	fromHeader := es.fromEmail
	if es.fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", es.fromName, es.fromEmail)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		fromHeader, to, subject, body)

	addr := fmt.Sprintf("%s:%d", es.smtpHost, es.smtpPort)
	err := smtp.SendMail(addr, auth, es.fromEmail, []string{to}, []byte(msg))

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

// renderTemplate renders an email template with data
func (es *EmailService) renderTemplate(templateName string, data EmailData) (string, error) {
	templates := map[string]string{
		"invoice_created": `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>New Invoice</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 640px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #2c3e50;">New Invoice from FrameWorks</h2>

        <p>Hello {{.TenantName}},</p>

        <p>A new invoice has been generated for your FrameWorks account:</p>

        <div style="background-color: #f8f9fa; padding: 20px; border-radius: 5px; margin: 20px 0;">
            <p><strong>Invoice ID:</strong> {{.InvoiceID}}</p>
            <p><strong>Amount:</strong> {{.Amount}} {{.Currency}}</p>
            <p><strong>Due Date:</strong> {{.DueDate.Format "January 2, 2006"}}</p>
        </div>

        {{if .LineItemGroups}}
        <h3 style="color: #2c3e50; margin-top: 30px;">Charges</h3>
        {{range .LineItemGroups}}
        <h4 style="color: #2c3e50; margin-top: 20px; margin-bottom: 8px;">
            {{if .ClusterName}}{{.ClusterName}}{{else}}Cluster {{.ClusterID}}{{end}}
            {{if eq .ClusterKind "tenant_private"}}<span style="font-size: 0.75em; color: #16a085; background: #e8f8f4; padding: 2px 8px; border-radius: 10px; margin-left: 8px;">Self-hosted</span>{{end}}
            {{if eq .ClusterKind "third_party_marketplace"}}<span style="font-size: 0.75em; color: #8e44ad; background: #f3eaf8; padding: 2px 8px; border-radius: 10px; margin-left: 8px;">Marketplace</span>{{end}}
            {{if eq .ClusterKind "platform_official"}}<span style="font-size: 0.75em; color: #2980b9; background: #eaf3fb; padding: 2px 8px; border-radius: 10px; margin-left: 8px;">Platform</span>{{end}}
        </h4>
        <table style="width: 100%; border-collapse: collapse; margin-bottom: 12px; font-size: 0.95em;">
            <tr style="background-color: #f8f9fa;">
                <th style="padding: 8px 10px; text-align: left; border-bottom: 1px solid #ddd;">Item</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Quantity</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Unit price</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Total</th>
            </tr>
            {{range .Lines}}
            <tr{{if .IsZeroPrice}} style="color: #666;"{{end}}>
                <td style="padding: 8px 10px; border-bottom: 1px solid #eee;">
                    {{.Description}}
                    {{if .PricingLabel}}<div style="font-size: 0.8em; color: #95a5a6;">{{.PricingLabel}}</div>{{end}}
                </td>
                <td style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #eee;">{{.Quantity}}</td>
                <td style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #eee;">{{.UnitPrice}} {{.Currency}}</td>
                <td style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #eee;">
                    {{if .IsZeroPrice}}<span style="color: #16a085; font-weight: 600;">Included</span>{{else}}{{.Total}} {{.Currency}}{{end}}
                </td>
            </tr>
            {{end}}
        </table>
        {{end}}
        {{end}}

        <p>Please log in to your account to view the invoice details and make payment:</p>

        <p style="text-align: center; margin: 30px 0;">
            <a href="{{.LoginURL}}" style="background-color: #3498db; color: white; padding: 12px 24px; text-decoration: none; border-radius: 5px; display: inline-block;">View Invoice</a>
        </p>

        <p>If you have any questions, please contact our support team.</p>

        <p>Best regards,<br>The FrameWorks Team</p>
    </div>
</body>
</html>`,

		"payment_success": `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Payment Confirmed</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 600px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #27ae60;">Payment Confirmed!</h2>
        
        <p>Hello {{.TenantName}},</p>
        
        <p>We've successfully received your payment. Thank you!</p>
        
        <div style="background-color: #d4edda; padding: 20px; border-radius: 5px; margin: 20px 0; border-left: 4px solid #27ae60;">
            <p><strong>Invoice ID:</strong> {{.InvoiceID}}</p>
            <p><strong>Amount Paid:</strong> {{.Amount}} {{.Currency}}</p>
            <p><strong>Payment Method:</strong> {{.PaymentMethod}}</p>
            <p><strong>Payment Date:</strong> {{.PaidAt.Format "January 2, 2006 at 3:04 PM"}}</p>
        </div>
        
        <p>Your account has been updated and all services remain active.</p>
        
        <p style="text-align: center; margin: 30px 0;">
            <a href="{{.LoginURL}}" style="background-color: #27ae60; color: white; padding: 12px 24px; text-decoration: none; border-radius: 5px; display: inline-block;">View Account</a>
        </p>
        
        <p>Thank you for using FrameWorks!</p>
        
        <p>Best regards,<br>The FrameWorks Team</p>
    </div>
</body>
</html>`,

		"payment_failed": `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Payment Failed</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 600px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #e74c3c;">Payment Failed</h2>
        
        <p>Hello {{.TenantName}},</p>
        
        <p>We were unable to process your payment for the following invoice:</p>
        
        <div style="background-color: #f8d7da; padding: 20px; border-radius: 5px; margin: 20px 0; border-left: 4px solid #e74c3c;">
            <p><strong>Invoice ID:</strong> {{.InvoiceID}}</p>
            <p><strong>Amount:</strong> {{.Amount}} {{.Currency}}</p>
            <p><strong>Payment Method:</strong> {{.PaymentMethod}}</p>
        </div>
        
        <p>Please check your payment method and try again, or contact your bank if the issue persists.</p>
        
        <p style="text-align: center; margin: 30px 0;">
            <a href="{{.LoginURL}}" style="background-color: #e74c3c; color: white; padding: 12px 24px; text-decoration: none; border-radius: 5px; display: inline-block;">Retry Payment</a>
        </p>
        
        <p>If you continue to experience issues, please contact our support team.</p>
        
        <p>Best regards,<br>The FrameWorks Team</p>
    </div>
</body>
</html>`,

		"overdue_reminder": `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Payment Reminder</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 600px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #f39c12;">Payment Reminder</h2>
        
        <p>Hello {{.TenantName}},</p>
        
        <p>This is a friendly reminder that the following invoice is now overdue:</p>
        
        <div style="background-color: #fff3cd; padding: 20px; border-radius: 5px; margin: 20px 0; border-left: 4px solid #f39c12;">
            <p><strong>Invoice ID:</strong> {{.InvoiceID}}</p>
            <p><strong>Amount Due:</strong> {{.Amount}} {{.Currency}}</p>
            <p><strong>Days Overdue:</strong> {{.DaysPastDue}} days</p>
        </div>
        
        <p>To avoid any service interruptions, please make payment as soon as possible.</p>
        
        <p style="text-align: center; margin: 30px 0;">
            <a href="{{.LoginURL}}" style="background-color: #f39c12; color: white; padding: 12px 24px; text-decoration: none; border-radius: 5px; display: inline-block;">Pay Now</a>
        </p>
        
        <p>If you have any questions or need assistance, please contact our support team.</p>
        
        <p>Best regards,<br>The FrameWorks Team</p>
    </div>
</body>
</html>`,
		"account_suspended": `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Account Suspended</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 600px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #e74c3c;">Account Suspended</h2>

        <p>Hello {{.TenantName}},</p>

        <p>Your account has been suspended because your prepaid balance is negative.</p>

        <div style="background-color: #f8d7da; padding: 20px; border-radius: 5px; margin: 20px 0; border-left: 4px solid #e74c3c;">
            <p><strong>Current Balance:</strong> {{.Balance}} {{.Currency}}</p>
        </div>

        <p>Please top up your balance to restore access and continue creating new resources.</p>

        <p style="text-align: center; margin: 30px 0;">
            <a href="{{.LoginURL}}" style="background-color: #e74c3c; color: white; padding: 12px 24px; text-decoration: none; border-radius: 5px; display: inline-block;">Go to Billing</a>
        </p>

        <p>If you believe this is a mistake, contact our support team.</p>

        <p>Best regards,<br>The FrameWorks Team</p>
    </div>
</body>
</html>`,
	}

	tmplContent, exists := templates[templateName]
	if !exists {
		return "", fmt.Errorf("template %s not found", templateName)
	}

	// Template functions for email rendering
	funcMap := template.FuncMap{
		"divFloat": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
	}

	tmpl, err := template.New(templateName).Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}
