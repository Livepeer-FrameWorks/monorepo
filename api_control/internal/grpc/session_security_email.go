package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/google/uuid"
)

type sessionSecurityEmail struct {
	Kind        string    `json:"kind"`
	Device      string    `json:"device"`
	ClientID    string    `json:"clientId"`
	IP          string    `json:"ip"`
	Location    string    `json:"location,omitempty"`
	Attribution string    `json:"-"`
	At          time.Time `json:"at"`
}

func sessionDeviceKey(clientID, label string) string {
	sum := sha256.Sum256([]byte(clientID + "\x00" + label))
	return hex.EncodeToString(sum[:])
}

// The owner lock serializes familiarity checks with sign-in, independently of
// display-label edits and the shorter ended-session retention window.
func (s *CommodoreServer) recordSessionDeviceTx(ctx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession, email string) error {
	queries := commodoredb.New(tx)
	familiar, err := queries.KnownSessionDevice(ctx, commodoredb.KnownSessionDeviceParams{TenantID: row.TenantID, UserID: row.UserID, DeviceKey: row.DeviceKey})
	if err != nil {
		return err
	}
	if err = queries.RememberSessionDevice(ctx, commodoredb.RememberSessionDeviceParams{TenantID: row.TenantID, UserID: row.UserID, DeviceKey: row.DeviceKey}); err != nil {
		return err
	}
	if familiar || strings.TrimSpace(email) == "" {
		return nil
	}
	return enqueueSessionSecurityEmailTx(ctx, tx, row, "sign_in")
}

func enqueueSessionSecurityEmailTx(ctx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession, kind string) error {
	if kind != "sign_in" && kind != "reuse" {
		return fmt.Errorf("invalid security email kind")
	}
	id, err := uuid.Parse(row.ID)
	if err != nil {
		return fmt.Errorf("invalid security email session ID: %w", err)
	}
	at := row.CreatedAt
	if kind == "reuse" && row.RevokedAt.Valid {
		at = row.RevokedAt.Time
	}
	data, err := json.Marshal(sessionSecurityEmail{Kind: kind, Device: row.DeviceLabel, ClientID: row.ClientID, IP: row.LastIp, At: at.UTC()})
	if err != nil {
		return err
	}
	return commodoredb.New(tx).EnqueueSecurityEmail(ctx, commodoredb.EnqueueSecurityEmailParams{TenantID: row.TenantID, UserID: row.UserID, Purpose: "security_" + kind + ":" + id.String(), Payload: data})
}

func (s *CommodoreServer) deliverSessionSecurityEmail(ctx context.Context, row accountEmailOutboxRow) error {
	var alert sessionSecurityEmail
	if err := json.Unmarshal(row.payload, &alert); err != nil {
		return fmt.Errorf("decode security email: %w", err)
	}
	if alert.Kind != "sign_in" && alert.Kind != "reuse" {
		return fmt.Errorf("invalid security email kind")
	}
	if !strings.HasPrefix(row.purpose, "security_"+alert.Kind+":") || alert.At.IsZero() {
		return fmt.Errorf("invalid security email payload")
	}
	recipient, err := commodoredb.New(s.db).GetAccountEmailRecipient(ctx, commodoredb.GetAccountEmailRecipientParams{UserID: row.userID, TenantID: row.tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load security email recipient: %w", err)
	}
	if strings.TrimSpace(recipient.Email) == "" {
		return nil
	}
	if !recipient.Verified {
		return fmt.Errorf("security email awaits address verification")
	}
	settings := s.settings()
	baseURL, err := validatedAccountEmailBaseURL(settings.Branding.WebAppURL, settings.Development)
	if err != nil {
		return err
	}
	if data := s.sessionGeoIP.Lookup(alert.IP); data != nil {
		alert.Location = strings.Trim(strings.Join([]string{data.City, data.CountryName}, ", "), ", ")
		alert.Attribution = s.sessionGeoIP.GetAttributionText()
	}
	message, err := settings.renderSessionSecurityEmail(baseURL, alert)
	if err != nil {
		return err
	}
	message.To = recipient.Email
	if s.securityEmailSendFn != nil {
		return s.securityEmailSendFn(ctx, message)
	}
	if settings.SMTPHost == "" {
		return fmt.Errorf("security email delivery awaits SMTP configuration")
	}
	return settings.sendMailContext(ctx, message)
}

func (r RuntimeSettings) renderSessionSecurityEmail(baseURL string, alert sessionSecurityEmail) (emailpkg.Message, error) {
	title := "New sign-in to FrameWorks"
	intro := "Your account was signed in on an unfamiliar device."
	notice := emailpkg.Notice{Title: "Did you start this sign-in?", Text: "If you do not recognize this device, review your sessions and sign out the device. Change your password if you suspect it was compromised."}
	if alert.Kind == "reuse" {
		title = "FrameWorks session token reuse detected"
		intro = "A refresh token from this session was reused. We ended the affected session to protect your account."
		notice = emailpkg.Notice{Title: "Review your account security", Text: "Sign in again on devices you trust. Review your other sessions and change your password if you did not cause this activity."}
	}
	location := alert.Location
	if location == "" {
		location = "Unavailable"
	}
	content := struct {
		Intro, Device, Client, IP, Location, At, Attribution string
		Action                                               emailpkg.Action
		Notice                                               emailpkg.Notice
	}{intro, alert.Device, alert.ClientID, alert.IP, location, alert.At.UTC().Format(time.RFC3339), alert.Attribution, emailpkg.Action{URL: strings.TrimRight(baseURL, "/") + "/account/security", Label: "Review account security"}, notice}
	layout := r.accountEmailLayout(title, "Account security", title, accountEmailContent{})
	layout.Content = content
	body, err := emailpkg.RenderLayout(layout, `<p>{{.Intro}}</p><dl><dt>Device</dt><dd>{{.Device}}</dd><dt>Client</dt><dd>{{.Client}}</dd><dt>IP address</dt><dd>{{.IP}}</dd><dt>Location</dt><dd>{{.Location}}</dd><dt>Time (UTC)</dt><dd>{{.At}}</dd></dl>{{if .Attribution}}<p>{{.Attribution}}</p>{{end}}{{template "action" .Action}}{{template "notice" .Notice}}`, nil)
	if err != nil {
		return emailpkg.Message{}, fmt.Errorf("render security email: %w", err)
	}
	return emailpkg.Message{Subject: title, HTMLBody: body, TextBody: emailpkg.HTMLToText(body), ReplyTo: r.Branding.Support()}, nil
}

func browserSessionLabel(agent string) string {
	browser := "Browser"
	for _, match := range []struct{ marker, name string }{{"Edg/", "Edge"}, {"EdgiOS/", "Edge"}, {"EdgA/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"}, {"Chrome/", "Chrome"}, {"CriOS/", "Chrome"}, {"Safari/", "Safari"}} {
		if strings.Contains(agent, match.marker) {
			browser = match.name
			break
		}
	}
	for _, match := range []struct{ marker, name string }{{"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Android", "Android"}, {"Windows NT", "Windows"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"}} {
		if strings.Contains(agent, match.marker) {
			return browser + " on " + match.name
		}
	}
	return browser
}
