package wireguard

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"github.com/vishvananda/netlink"
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
// link, bringing it up, and reconciling its IP address. It is separate
// from wgctrlClient because wgctrl handles only WireGuard device state,
// not link or address management. The interface is also the test seam:
// linuxManager holds it as a field so unit tests inject a fake.
type linkOps interface {
	EnsureLink(name string) error
	LinkUp(name string) error
	EnsureAddress(name string, addr netip.Prefix) error
}

// netlinkLinkOps is the production linkOps backed by the kernel netlink
// ABI via github.com/vishvananda/netlink. It is the same library used by
// containerd, CNI plugins, and Kubernetes networking.
type netlinkLinkOps struct{}

// wireguardLinkType is the kernel link type netlink reports for in-kernel
// WireGuard interfaces. Anything else under the same name is a foreign
// interface we must not touch.
const wireguardLinkType = "wireguard"

func (netlinkLinkOps) EnsureLink(name string) error {
	link, err := netlink.LinkByName(name)
	if err == nil {
		if t := link.Type(); t != wireguardLinkType {
			return fmt.Errorf("link %s exists but is type %q, not %q", name, t, wireguardLinkType)
		}
		return nil
	}
	if !errors.As(err, new(netlink.LinkNotFoundError)) {
		return fmt.Errorf("look up link %s: %w", name, err)
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: attrs}); err != nil {
		return fmt.Errorf("add wireguard link %s: %w", name, err)
	}
	return nil
}

func (netlinkLinkOps) LinkUp(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("look up link %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("set %s up: %w", name, err)
	}
	return nil
}

func (netlinkLinkOps) EnsureAddress(name string, addr netip.Prefix) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("look up link %s: %w", name, err)
	}
	addrs, err := netlink.AddrList(link, syscall.AF_INET)
	if err != nil {
		return fmt.Errorf("list addresses on %s: %w", name, err)
	}

	desired := prefixToAddr(addr)
	toDelete, addDesired := reconcileAddresses(addrs, desired)
	for i := range toDelete {
		if delErr := netlink.AddrDel(link, &toDelete[i]); delErr != nil {
			return fmt.Errorf("remove stale address %s from %s: %w", toDelete[i].IPNet, name, delErr)
		}
	}
	if addDesired {
		if addErr := netlink.AddrAdd(link, desired); addErr != nil {
			return fmt.Errorf("add address %s on %s: %w", addr, name, addErr)
		}
	}
	return nil
}

// reconcileAddresses computes the netlink mutations that converge the
// link's IPv4 address set to exactly {desired}: any existing address that
// doesn't match desired is returned in toDelete, and addDesired is true
// when desired itself is missing from existing.
func reconcileAddresses(existing []netlink.Addr, desired *netlink.Addr) (toDelete []netlink.Addr, addDesired bool) {
	addDesired = true
	for _, e := range existing {
		if addrEqual(e, desired) {
			addDesired = false
			continue
		}
		toDelete = append(toDelete, e)
	}
	return toDelete, addDesired
}

// prefixToAddr converts a netip.Prefix into the *netlink.Addr shape the
// netlink package expects. Self addresses are validated as IPv4 by
// ValidateForApply, so we always emit a /32 IPv4 mask here.
func prefixToAddr(p netip.Prefix) *netlink.Addr {
	return &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   p.Addr().AsSlice(),
			Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
		},
	}
}

func addrEqual(a netlink.Addr, b *netlink.Addr) bool {
	if a.IPNet == nil || b == nil || b.IPNet == nil {
		return false
	}
	if !a.IP.Equal(b.IP) {
		return false
	}
	aOnes, aBits := a.Mask.Size()
	bOnes, bBits := b.Mask.Size()
	return aOnes == bOnes && aBits == bBits
}

type linuxManager struct {
	interfaceName string
	client        wgctrlClient
	link          linkOps
}

func newLinuxManager(interfaceName string) Manager {
	return &linuxManager{
		interfaceName: interfaceName,
		link:          netlinkLinkOps{},
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
