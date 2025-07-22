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
	LoginURL      string
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
func (es *EmailService) SendInvoiceCreatedEmail(tenantEmail, tenantName, invoiceID string, amount float64, currency string, dueDate time.Time) error {
	if !es.IsConfigured() {
		es.logger.Warn("Email service not configured, skipping invoice created email")
		return nil
	}

	subject := fmt.Sprintf("New Invoice %s - FrameWorks", invoiceID)

	data := EmailData{
		TenantName: tenantName,
		InvoiceID:  invoiceID,
		Amount:     amount,
		Currency:   currency,
		DueDate:    dueDate,
		LoginURL:   os.Getenv("BASE_URL") + "/login",
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
		LoginURL:      os.Getenv("BASE_URL") + "/login",
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
		LoginURL:      os.Getenv("BASE_URL") + "/login",
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
		LoginURL:    os.Getenv("BASE_URL") + "/login",
	}

	body, err := es.renderTemplate("overdue_reminder", data)
	if err != nil {
		return fmt.Errorf("failed to render overdue reminder template: %w", err)
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
	}

	tmplContent, exists := templates[templateName]
	if !exists {
		return "", fmt.Errorf("template %s not found", templateName)
	}

	tmpl, err := template.New(templateName).Parse(tmplContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}
