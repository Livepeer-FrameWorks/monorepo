package notify

import (
	"context"
	"errors"

	"frameworks/pkg/logging"
)

type Dispatcher struct {
	email    *EmailNotifier
	mcp      *MCPNotifier
	defaults PreferenceDefaults
	logger   logging.Logger
}

type DispatcherConfig struct {
	EmailNotifier *EmailNotifier
	MCPNotifier   *MCPNotifier
	Defaults      PreferenceDefaults
	Logger        logging.Logger
}

func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	return &Dispatcher{
		email:    cfg.EmailNotifier,
		mcp:      cfg.MCPNotifier,
		defaults: cfg.Defaults,
		logger:   cfg.Logger,
	}
}

func (d *Dispatcher) Notify(ctx context.Context, report Report) error {
	prefs := ResolvePreferences(d.defaults, report.Preferences)
	var errs []error

	if prefs.Email {
		if d.email == nil {
			d.logger.WithField("tenant_id", report.TenantID).Warn("Email notification enabled but notifier missing")
		} else if err := d.email.Notify(ctx, report); err != nil {
			errs = append(errs, err)
		}
	}

	if prefs.MCP {
		if d.mcp == nil {
			d.logger.WithField("tenant_id", report.TenantID).Warn("MCP notification enabled but notifier missing")
		} else if err := d.mcp.Notify(ctx, report); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}
