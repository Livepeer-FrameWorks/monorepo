package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/credentials"
	"frameworks/cli/pkg/clients/bridge"
	"github.com/spf13/cobra"
)

type authCommandOptions struct {
	Store             func() credentials.Store
	Env               fwcfg.Env
	EnvironmentBridge func() (string, error)
}

type commandSession struct {
	credentials.SessionAuth
	store  credentials.Store
	client *bridge.Client
}

func newAuthCmd() *cobra.Command { return newAuthCmdWithOptions(authCommandOptions{}) }

func newAuthCmdWithOptions(options authCommandOptions) *cobra.Command {
	if options.Store == nil {
		options.Store = credentials.DefaultStore
	}
	if options.Env == nil {
		options.Env = fwcfg.OSEnv{}
	}
	if options.EnvironmentBridge == nil {
		options.EnvironmentBridge = func() (string, error) { return resolveLoginBridgeURL("") }
	}
	command := &cobra.Command{
		Use:   "auth",
		Short: "Inspect and manage your sign-in sessions",
		Long: `Manage your own sign-in sessions on the Bridge where you logged in.

Stored CLI credentials use their original login Bridge. FW_USER_TOKEN uses the
active context's Bridge and is never refreshed, stored or overwritten here.
Session management requires platform v0.3.15 or newer.`,
	}
	command.AddCommand(&cobra.Command{Use: "status", Short: "Show your identity, session and expiry", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			session, err := options.load(ctx)
			if err != nil {
				return err
			}
			status, err := readAuthStatus(ctx, session)
			if err != nil {
				return err
			}
			if fwcfg.GetRuntimeOverrides().OutputJSON || output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "User: %s\nTenant: %s\nBridge: %s\nCredentials: %s\n", terminalAuthValue(status.UserID), terminalAuthValue(status.TenantID), status.BridgeURL, status.Source)
			if status.Email != "" {
				fmt.Fprintf(out, "Email: %s\n", terminalAuthValue(status.Email))
			}
			if status.Session != nil {
				fmt.Fprintf(out, "Session: %s (%s)\nExpires: %s\n", terminalAuthValue(status.Session.Label), status.Session.ID, status.Session.ExpiresAt.Format(time.RFC3339))
			} else {
				fmt.Fprintln(out, "Session: legacy sign-in; renew through its owning application or run 'frameworks login'")
			}
			return nil
		},
	})
	sessions := &cobra.Command{Use: "sessions", Short: "List or revoke your sign-in sessions"}
	command.AddCommand(sessions)
	var first int
	var after string
	list := &cobra.Command{Use: "list", Short: "List signed-in browsers, CLI and desktop sessions", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if first < 1 || first > 100 {
				return fmt.Errorf("--first must be between 1 and 100")
			}
			session, err := options.load(cmd.Context())
			if err != nil {
				return err
			}
			var cursor *string
			if after != "" {
				cursor = &after
			}
			page, err := session.client.AuthSessions(cmd.Context(), session.Token, first, cursor)
			if err != nil {
				return err
			}
			if fwcfg.GetRuntimeOverrides().OutputJSON || output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(page)
			}
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(writer, "ID\tCLIENT\tLABEL\tLAST ACTIVE\tEXPIRES\tCURRENT")
			for _, row := range page.Nodes {
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%t\n", terminalAuthValue(row.ID), terminalAuthValue(row.ClientKind), terminalAuthValue(row.Label), row.LastActiveAt.Format(time.RFC3339), row.ExpiresAt.Format(time.RFC3339), row.Current)
			}
			if err = writer.Flush(); err != nil {
				return err
			}
			if page.PageInfo.HasNextPage && page.PageInfo.EndCursor != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "More sessions: frameworks auth sessions list --after %s\n", strconv.Quote(*page.PageInfo.EndCursor))
			}
			return nil
		},
	}
	list.Flags().IntVar(&first, "first", 100, "sessions per page (1–100)")
	list.Flags().StringVar(&after, "after", "", "continue after an opaque cursor")
	sessions.AddCommand(list)
	sessions.AddCommand(&cobra.Command{Use: "revoke <id>", Short: "Sign out one of your sessions", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := options.load(cmd.Context())
			if err != nil {
				return err
			}
			result, err := session.client.RevokeAuthSession(cmd.Context(), session.Token, args[0])
			if err != nil {
				return err
			}
			cleared := false
			if result.Current && !session.Environment {
				cleared, err = credentials.ClearRevokedSession(cmd.Context(), session.store, session.BridgeURL, args[0])
				if err != nil {
					return fmt.Errorf("session revoked but local credential cleanup failed: %w", err)
				}
			}
			if fwcfg.GetRuntimeOverrides().OutputJSON || output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					*bridge.SessionRevoked
					CredentialsCleared bool `json:"credentialsCleared"`
				}{result, cleared})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked session %s\n", terminalAuthValue(args[0]))
			return nil
		},
	})
	sessions.AddCommand(&cobra.Command{Use: "revoke-others", Short: "Sign out other devices and keep this session", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			session, err := options.load(cmd.Context())
			if err != nil {
				return err
			}
			count, err := session.client.RevokeOtherAuthSessions(cmd.Context(), session.Token)
			if err != nil {
				return err
			}
			if fwcfg.GetRuntimeOverrides().OutputJSON || output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]int{"revokedCount": count})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %d other session(s)\n", count)
			return nil
		},
	})
	return command
}

func (options authCommandOptions) load(ctx context.Context) (*commandSession, error) {
	var store credentials.Store
	if options.Env.Get(credentials.EnvUserToken) == "" {
		store = options.Store()
	}
	auth, err := credentials.ResolveSessionAuth(ctx, options.Env, store)
	if err != nil {
		return nil, err
	}
	if auth.Environment {
		auth.BridgeURL, err = options.EnvironmentBridge()
		if err != nil {
			return nil, err
		}
	}
	endpoint, err := url.Parse(auth.BridgeURL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("invalid login Bridge; run 'frameworks login' with a valid Bridge URL")
	}
	session := &commandSession{SessionAuth: auth, store: store}
	var refresh func(context.Context, string, string) (string, error)
	if !auth.Environment {
		refresh = func(ctx context.Context, origin, token string) (string, error) {
			fresh, refreshErr := credentials.RefreshStoredUserAuth(ctx, store, origin, token)
			if refreshErr == nil && fresh != "" {
				session.Token = fresh
			}
			return fresh, refreshErr
		}
	}
	session.client = bridge.NewWithRefresh(auth.BridgeURL, refresh)
	return session, nil
}

type authStatus struct {
	Authenticated   bool                `json:"authenticated"`
	UserID          string              `json:"userId"`
	TenantID        string              `json:"tenantId"`
	Email           string              `json:"email,omitempty"`
	BridgeURL       string              `json:"bridgeUrl"`
	Source          string              `json:"credentialSource"`
	AccessExpiresAt *time.Time          `json:"accessExpiresAt,omitempty"`
	Session         *bridge.AuthSession `json:"session,omitempty"`
	Legacy          bool                `json:"legacy"`
}

func readAuthStatus(ctx context.Context, session *commandSession) (*authStatus, error) {
	page, err := session.client.AuthSessions(ctx, session.Token, 100, nil)
	if err != nil {
		return nil, err
	}
	// Claims supply display data only after Bridge authenticates the credential.
	parts := strings.Split(session.Token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("signed-in identity is unreadable; run 'frameworks login'")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	var identity struct {
		UserID    string `json:"user_id"`
		TenantID  string `json:"tenant_id"`
		Email     string `json:"email"`
		SessionID string `json:"sid"`
		Expires   int64  `json:"exp"`
	}
	if err != nil || json.Unmarshal(data, &identity) != nil || identity.UserID == "" || identity.TenantID == "" {
		return nil, fmt.Errorf("signed-in identity is unreadable; run 'frameworks login'")
	}
	status := &authStatus{Authenticated: true, UserID: identity.UserID, TenantID: identity.TenantID, Email: identity.Email, BridgeURL: session.BridgeURL, Source: "cli_store", Legacy: identity.SessionID == ""}
	if session.Environment {
		status.Source = credentials.EnvUserToken
	}
	if identity.Expires > 0 {
		expires := time.Unix(identity.Expires, 0).UTC()
		status.AccessExpiresAt = &expires
	}
	var after string
	for {
		for _, row := range page.Nodes {
			if row.Current {
				status.Session = &row
				return status, nil
			}
		}
		if status.Legacy {
			return status, nil
		}
		if !page.PageInfo.HasNextPage {
			return nil, fmt.Errorf("current session is no longer listed; renew or run 'frameworks login'")
		}
		if page.PageInfo.EndCursor == nil || *page.PageInfo.EndCursor == "" || *page.PageInfo.EndCursor == after {
			return nil, fmt.Errorf("bridge returned a non-advancing session cursor")
		}
		after = *page.PageInfo.EndCursor
		page, err = session.client.AuthSessions(ctx, session.Token, 100, &after)
		if err != nil {
			return nil, err
		}
	}
}

func terminalAuthValue(value string) string {
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return strconv.Quote(value)
	}
	return value
}
