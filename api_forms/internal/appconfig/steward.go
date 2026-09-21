// Package appconfig holds the typed startup configuration of Steward.
// scripts/configref generates the operator configuration reference from these
// structs.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// DefaultContactRecipient receives contact form submissions when TO_EMAIL is
// empty.
const DefaultContactRecipient = "contact@frameworks.network"

// Steward is the startup configuration of the Steward forms service. Every
// field applies at startup.
//
//configref:service steward cmd=cmd/steward
type Steward struct {
	config.HTTPListen
	config.HTTPRuntime
	config.Logging
	config.GRPCClientTLS
	config.EmailBranding

	TurnstileFormsSecretKey string `env:"TURNSTILE_FORMS_SECRET_KEY" secret:"true" desc:"Cloudflare Turnstile secret key for contact and subscribe forms. Empty disables Turnstile verification." introduced:"v0.3.0"`

	SMTPHost     string `env:"SMTP_HOST" desc:"SMTP server host for contact form emails. Empty reports the configuration health check as unhealthy." introduced:"v0.3.0"`
	SMTPPort     string `env:"SMTP_PORT" default:"587" desc:"SMTP server port for contact form emails." introduced:"v0.3.0"`
	SMTPUser     string `env:"SMTP_USER" desc:"SMTP user for contact form emails." introduced:"v0.3.0"`
	SMTPPassword string `env:"SMTP_PASSWORD" secret:"true" desc:"Password for SMTP_USER." introduced:"v0.3.0"`
	FromEmail    string `env:"FROM_EMAIL" default:"noreply@frameworks.network" desc:"Sender address for contact form emails." introduced:"v0.3.0"`

	FromName          string `env:"FROM_NAME" default:"FrameWorks" desc:"Sender display name for contact form emails." introduced:"v0.3.10"`
	SMTPAllowInsecure bool   `env:"SMTP_ALLOW_INSECURE" default:"false" desc:"Sends contact form emails to an SMTP server that does not offer STARTTLS. For an isolated development or test relay only; never set it in production." introduced:"v0.3.10"`

	ToEmail               string `env:"TO_EMAIL" desc:"Recipient of contact form submissions. Empty sends to contact@frameworks.network and reports the configuration health check as unhealthy." introduced:"v0.3.0"`
	EmailSubjectPrefix    string `env:"EMAIL_SUBJECT_PREFIX" default:"Contact Form" desc:"Subject prefix of contact form emails." introduced:"v0.3.0"`
	ContactSuccessMessage string `env:"CONTACT_SUCCESS_MESSAGE" default:"Thank you for your message! We'll get back to you soon." desc:"Message returned to the visitor after a contact form submission is sent." introduced:"v0.3.0"`

	ListmonkURL          string `env:"LISTMONK_URL" desc:"Listmonk base URL. Together with LISTMONK_API_USERNAME and LISTMONK_API_TOKEN it enables the subscribe endpoint." introduced:"v0.3.0"`
	ListmonkAPIUsername  string `env:"LISTMONK_API_USERNAME" desc:"Listmonk API user for newsletter subscriptions." introduced:"v0.3.0"`
	ListmonkAPIToken     string `env:"LISTMONK_API_TOKEN" secret:"true" desc:"Listmonk API token for LISTMONK_API_USERNAME." introduced:"v0.3.0"`
	DefaultMailingListID int    `env:"DEFAULT_MAILING_LIST_ID" default:"1" desc:"Listmonk list ID that newsletter subscriptions are added to." introduced:"v0.3.0"`

	DecklogGRPCAddr          string `env:"DECKLOG_GRPC_ADDR" desc:"Decklog gRPC address for operator activity events about contact and subscribe submissions. Empty disables the events." introduced:"v0.3.9"`
	DecklogGRPCTLSServerName string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.9"`
	ServiceToken             string `env:"SERVICE_TOKEN" secret:"true" desc:"Service token sent on Decklog calls. Used only when DECKLOG_GRPC_ADDR is set." introduced:"v0.3.9"`
	ClusterID                string `env:"CLUSTER_ID" desc:"Cluster ID stamped as the source cluster on operator activity events." introduced:"v0.3.9"`
	Region                   string `env:"REGION" desc:"Region label stamped as the source region on operator activity events." introduced:"v0.3.9"`
}

// ContactRecipient returns TO_EMAIL, or DefaultContactRecipient when it is
// empty.
func (c *Steward) ContactRecipient() string {
	if c.ToEmail != "" {
		return c.ToEmail
	}
	return DefaultContactRecipient
}
