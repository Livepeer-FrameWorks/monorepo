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
	UsageDetails  map[string]interface{}
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

// SendInvoiceCreatedEmail sends notification when a new invoice is created
func (es *EmailService) SendInvoiceCreatedEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency string, dueDate time.Time, usageDetails map[string]interface{}) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping invoice created email")
		return nil
	}

	subject := fmt.Sprintf("New Invoice %s - FrameWorks", invoiceID)

	data := EmailData{
		TenantName:   tenantName,
		InvoiceID:    invoiceID,
		Amount:       amount,
		Currency:     currency,
		DueDate:      dueDate,
		LoginURL:     os.Getenv("WEBAPP_PUBLIC_URL") + "/login",
		UsageDetails: usageDetails,
	}

	body, err := es.renderTemplate("invoice_created", data)
	if err != nil {
		return fmt.Errorf("failed to render invoice created template: %w", err)
	}

	return es.sendEmail(tenantEmail, subject, body)
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
    <div style="max-width: 600px; margin: 0 auto; padding: 20px;">
        <h2 style="color: #2c3e50;">New Invoice from FrameWorks</h2>
        
        <p>Hello {{.TenantName}},</p>
        
        <p>A new invoice has been generated for your FrameWorks account:</p>
        
        <div style="background-color: #f8f9fa; padding: 20px; border-radius: 5px; margin: 20px 0;">
            <p><strong>Invoice ID:</strong> {{.InvoiceID}}</p>
            <p><strong>Amount:</strong> {{.Amount}} {{.Currency}}</p>
            <p><strong>Due Date:</strong> {{.DueDate.Format "January 2, 2006"}}</p>
        </div>

        {{if .UsageDetails}}
        <h3 style="color: #2c3e50; margin-top: 30px;">Usage Breakdown</h3>
        <table style="width: 100%; border-collapse: collapse; margin-bottom: 20px;">
            <tr style="background-color: #eee;">
                <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Metric</th>
                <th style="padding: 10px; text-align: right; border-bottom: 1px solid #ddd;">Value</th>
            </tr>
            {{if .UsageDetails.viewer_hours}}
            <tr>
                <td style="padding: 10px; border-bottom: 1px solid #ddd;">Viewer Hours</td>
                <td style="padding: 10px; text-align: right; border-bottom: 1px solid #ddd;">{{printf "%.2f" .UsageDetails.viewer_hours}} hrs</td>
            </tr>
            {{end}}
            {{if .UsageDetails.egress_gb}}
            <tr>
                <td style="padding: 10px; border-bottom: 1px solid #ddd;">Bandwidth (Egress)</td>
                <td style="padding: 10px; text-align: right; border-bottom: 1px solid #ddd;">{{printf "%.2f" .UsageDetails.egress_gb}} GB</td>
            </tr>
            {{end}}
            {{if .UsageDetails.average_storage_gb}}
            <tr>
                <td style="padding: 10px; border-bottom: 1px solid #ddd;">Avg Storage</td>
                <td style="padding: 10px; text-align: right; border-bottom: 1px solid #ddd;">{{printf "%.2f" .UsageDetails.average_storage_gb}} GB</td>
            </tr>
            {{end}}
            {{if .UsageDetails.unique_users}}
            <tr>
                <td style="padding: 10px; border-bottom: 1px solid #ddd;">Unique Viewers</td>
                <td style="padding: 10px; text-align: right; border-bottom: 1px solid #ddd;">{{.UsageDetails.unique_users}}</td>
            </tr>
            {{end}}
        </table>

        {{if .UsageDetails.geo_breakdown}}
        <h4 style="color: #2c3e50; margin-top: 20px;">Top Regions</h4>
        <table style="width: 100%; border-collapse: collapse; margin-bottom: 20px; font-size: 0.9em;">
            <tr style="background-color: #f8f9fa;">
                <th style="padding: 8px 10px; text-align: left; border-bottom: 1px solid #ddd;">Country</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Viewers</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Hours</th>
            </tr>
            {{range .UsageDetails.geo_breakdown}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">{{.country_code}}</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{.viewer_count}} ({{printf "%.1f" .percentage}}%)</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" .viewer_hours}}h</td>
            </tr>
            {{end}}
        </table>
        {{end}}

        {{if or .UsageDetails.livepeer_h264_seconds .UsageDetails.native_av_h264_seconds}}
        <h4 style="color: #2c3e50; margin-top: 20px;">Processing / Transcoding</h4>
        <table style="width: 100%; border-collapse: collapse; margin-bottom: 20px; font-size: 0.9em;">
            <tr style="background-color: #f8f9fa;">
                <th style="padding: 8px 10px; text-align: left; border-bottom: 1px solid #ddd;">Codec</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Minutes</th>
                <th style="padding: 8px 10px; text-align: right; border-bottom: 1px solid #ddd;">Rate</th>
            </tr>
            {{if .UsageDetails.livepeer_h264_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Livepeer H264</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.livepeer_h264_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">1.0x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.livepeer_vp9_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Livepeer VP9</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.livepeer_vp9_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">1.5x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.livepeer_av1_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Livepeer AV1</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.livepeer_av1_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">2.0x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.livepeer_hevc_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Livepeer HEVC</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.livepeer_hevc_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">1.5x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.native_av_h264_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Native AV H264</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.native_av_h264_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">1.0x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.native_av_vp9_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Native AV VP9</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.native_av_vp9_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">1.5x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.native_av_av1_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Native AV AV1</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.native_av_av1_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">2.0x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.native_av_hevc_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee;">Native AV HEVC</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">{{printf "%.1f" (divFloat .UsageDetails.native_av_hevc_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee;">1.5x</td>
            </tr>
            {{end}}
            {{if .UsageDetails.native_av_aac_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee; color: #666;">Audio (AAC)</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee; color: #666;">{{printf "%.1f" (divFloat .UsageDetails.native_av_aac_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee; color: #27ae60;">FREE</td>
            </tr>
            {{end}}
            {{if .UsageDetails.native_av_opus_seconds}}
            <tr>
                <td style="padding: 5px 10px; border-bottom: 1px solid #eee; color: #666;">Audio (Opus)</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee; color: #666;">{{printf "%.1f" (divFloat .UsageDetails.native_av_opus_seconds 60)}} min</td>
                <td style="padding: 5px 10px; text-align: right; border-bottom: 1px solid #eee; color: #27ae60;">FREE</td>
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
