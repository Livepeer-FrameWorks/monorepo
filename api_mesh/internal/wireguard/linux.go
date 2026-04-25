package wireguard

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// wgctrlClient is the subset of *wgctrl.Client the manager uses. The
// interface exists so tests can swap in a fake without a real netlink
// socket; production paths always pass a *wgctrl.Client.
type wgctrlClient interface {
	ConfigureDevice(name string, cfg wgtypes.Config) error
	Close() error
}

// linkOps owns Linux link/address lifecycle: creating the wireguard-typed
// link, bringing it up, and reconciling its IP address. It is split from
// wgctrlClient because wgctrl handles only WireGuard device state, not
// link or address management. The seam exists so the netlink swap can
// replace the shell implementation without touching Apply.
type linkOps interface {
	EnsureLink(name string) error
	LinkUp(name string) error
	EnsureAddress(name string, addr netip.Prefix) error
}

type shellLinkOps struct{}

func (shellLinkOps) EnsureLink(name string) error {
	ctx := context.Background()
	if _, err := exec.CommandContext(ctx, "ip", "link", "show", name).Output(); err == nil {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "ip", "link", "add", "dev", name, "type", "wireguard").CombinedOutput(); err != nil {
		return fmt.Errorf("create interface %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (shellLinkOps) LinkUp(name string) error {
	ctx := context.Background()
	if out, err := exec.CommandContext(ctx, "ip", "link", "set", "up", "dev", name).CombinedOutput(); err != nil {
		return fmt.Errorf("set %s up: %w: %s", name, err, string(out))
	}
	return nil
}

func (shellLinkOps) EnsureAddress(name string, addr netip.Prefix) error {
	ctx := context.Background()
	addrText := addr.String()
	out, err := exec.CommandContext(ctx, "ip", "-o", "-4", "addr", "show", name).Output()
	if err != nil {
		return fmt.Errorf("list addresses on %s: %w", name, err)
	}
	if strings.Contains(string(out), addrText) {
		return nil
	}
	if flushOut, flushErr := exec.CommandContext(ctx, "ip", "addr", "flush", "dev", name).CombinedOutput(); flushErr != nil {
		return fmt.Errorf("flush addresses on %s: %w: %s", name, flushErr, string(flushOut))
	}
	if addOut, addErr := exec.CommandContext(ctx, "ip", "addr", "add", addrText, "dev", name).CombinedOutput(); addErr != nil {
		return fmt.Errorf("add address %s on %s: %w: %s", addrText, name, addErr, string(addOut))
	}
	return nil
}

type linuxManager struct {
	interfaceName string
	client        wgctrlClient
	link          linkOps
}

func newLinuxManager(interfaceName string) Manager {
	return &linuxManager{
		interfaceName: interfaceName,
		link:          shellLinkOps{},
	}
}

func (m *linuxManager) Init() error {
	if err := m.link.EnsureLink(m.interfaceName); err != nil {
		return err
	}
	if err := m.link.LinkUp(m.interfaceName); err != nil {
		return err
	}
	if m.client == nil {
		c, err := wgctrl.New()
		if err != nil {
			return fmt.Errorf("open wgctrl: %w", err)
		}
		m.client = c
	}
	return nil
}

func (m *linuxManager) Apply(cfg Config) error {
	if m.client == nil {
		return fmt.Errorf("wireguard manager: Init must be called before Apply")
	}
	if err := ValidateForApply(cfg); err != nil {
		return fmt.Errorf("wireguard policy: %w", err)
	}
	if err := m.client.ConfigureDevice(m.interfaceName, cfg.toWGTypes()); err != nil {
		return fmt.Errorf("configure %s: %w", m.interfaceName, err)
	}
	if err := m.link.EnsureAddress(m.interfaceName, cfg.Address); err != nil {
		return err
	}
	return nil
}

func (m *linuxManager) Close() error {
	if m.client == nil {
		return nil
	}
	return m.client.Close()
}
