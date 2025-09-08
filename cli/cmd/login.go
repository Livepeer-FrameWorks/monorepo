package cmd

import (
	"bufio"
	"context"
	"fmt"
	fwcfg "frameworks/cli/internal/config"
	commodore "frameworks/pkg/clients/commodore"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newLoginCmd() *cobra.Command {
	var email string
	var password string
	var jwtToken string
	var serviceToken string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate and store JWT/service token in current context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := fwcfg.Load()
			if err != nil {
				return err
			}
			ctx := fwcfg.GetCurrent(cfg)

			changed := false
			// Direct tokens
			if strings.TrimSpace(jwtToken) != "" {
				ctx.Auth.JWT = strings.TrimSpace(jwtToken)
				changed = true
			}
			if strings.TrimSpace(serviceToken) != "" {
				ctx.Auth.ServiceToken = strings.TrimSpace(serviceToken)
				changed = true
			}

			// Email/password via Gateway -> Commodore /auth/login
			if strings.TrimSpace(email) != "" && strings.TrimSpace(password) == "" {
				fmt.Fprint(cmd.OutOrStdout(), "Password: ")
				r := bufio.NewReader(os.Stdin)
				pw, _ := r.ReadString('\n')
				password = strings.TrimSpace(pw)
			}
			if strings.TrimSpace(email) != "" && strings.TrimSpace(password) != "" {
				base := strings.TrimRight(ctx.Endpoints.GatewayURL, "/") + "/auth"
				client := commodore.NewClient(commodore.Config{BaseURL: base, Timeout: 10 * time.Second})
				cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				resp, err := client.Login(cctx, email, password)
				if err != nil {
					return fmt.Errorf("login failed: %w", err)
				}
				if resp == nil || resp.Token == "" {
					return fmt.Errorf("login failed: empty token")
				}
				ctx.Auth.JWT = resp.Token
				changed = true
				fmt.Fprintln(cmd.OutOrStdout(), "Login successful; JWT stored in context.")
			}

			if !changed {
				return fmt.Errorf("no credentials provided; use --email/--password or --jwt or --service-token")
			}
			// Save back
			cfg.Contexts[ctx.Name] = ctx
			if err := fwcfg.Save(cfg, path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated credentials in context %q\n", ctx.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "email for Gateway login")
	cmd.Flags().StringVar(&password, "password", "", "password for Gateway login (prompted if omitted)")
	cmd.Flags().StringVar(&jwtToken, "jwt", "", "set JWT directly")
	cmd.Flags().StringVar(&serviceToken, "service-token", "", "set SERVICE_TOKEN for provider/service operations")
	return cmd
}
