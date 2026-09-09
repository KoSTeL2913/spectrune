package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"

	awgconn "github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"

	"spectrune/internal/netstack"
)

// outboundTunnel holds our own, fully self-contained AmneziaWG connection
// to the user's VPN peer — separate from the intercepting Wintun adapter.
// It exists purely in-memory (gVisor netstack, no second real Windows
// adapter), so it never touches the OS routing table and can't loop back
// into the intercepting bridge.
type outboundTunnel struct {
	dev *device.Device
	net *netstack.Net

	// includedApps is read on every intercepted connection (matches) and
	// can be rewritten live while connected (updateIncludedApps, driven by
	// Service.SaveProfile editing the currently-active profile) — editing
	// the Apps list no longer requires a full Disconnect/Connect, which
	// used to tear down and rebuild the whole adapter/tunnel just to
	// change which apps route through it, dropping every other
	// connection along with it.
	mu           sync.RWMutex
	includedApps map[string]bool // lower-cased absolute exe paths
}

func newOutboundTunnel(cfg *conf.Config, realIfaceIndex uint32) (*outboundTunnel, error) {
	var addrs []netip.Addr
	for _, a := range cfg.Interface.Addresses {
		if ip, ok := netip.AddrFromSlice(a.IP.To4()); ok {
			addrs = append(addrs, ip)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("config has no IPv4 Address")
	}

	var dns []netip.Addr
	for _, d := range cfg.Interface.DNS {
		if ip, ok := netip.AddrFromSlice(d.To4()); ok {
			dns = append(dns, ip)
		}
	}
	if len(dns) == 0 {
		dns = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}

	tunDev, tnet, err := netstack.CreateNetTUN(addrs, dns, 1420)
	if err != nil {
		return nil, fmt.Errorf("CreateNetTUN: %w", err)
	}

	bind := awgconn.NewDefaultBind()
	// LogLevelError, not Verbose: verbose logs every single packet/handshake/
	// keepalive continuously for the session's lifetime. That was free while
	// this went nowhere (no console, unredirected), but once launchAsptBridge
	// started redirecting stdout/stderr to a real log file, it became
	// synchronous disk I/O on every packet — confirmed as the cause of
	// generally slow/laggy tunneled traffic 2026-09-02.
	dev := device.NewDevice(tunDev, bind, device.NewLogger(device.LogLevelError, "[out] "))

	uapiConf, dnsErr := cfg.ToUAPI()
	if dnsErr != nil {
		dev.Close()
		return nil, fmt.Errorf("ToUAPI (endpoint DNS resolution): %w", dnsErr)
	}
	if err := dev.IpcSet(uapiConf); err != nil {
		dev.Close()
		return nil, fmt.Errorf("IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("Up: %w", err)
	}

	// The bind's real socket only exists once the device is up (IpcSet
	// opens it) — pinning any earlier operates on a not-yet-open socket
	// and fails with "use of closed network connection".
	if pinner, ok := bind.(awgconn.BindSocketToInterface); ok {
		if err := pinner.BindSocketToInterface4(realIfaceIndex, false); err != nil {
			dev.Close()
			return nil, fmt.Errorf("pinning tunnel UDP socket to real interface: %w", err)
		}
	}

	included := make(map[string]bool, len(cfg.Interface.IncludedApps))
	for _, p := range cfg.Interface.IncludedApps {
		included[strings.ToLower(filepath.Clean(p))] = true
	}

	return &outboundTunnel{dev: dev, net: tnet, includedApps: included}, nil
}

// matches decides whether a given process's traffic should go through the
// tunnel. An empty IncludedApps list means the profile has no per-app
// selection at all — treated as full-tunnel (route everything), matching
// how every other VPN client behaves by default; per-app split is opt-in
// via the Apps picker, not opt-out.
func (t *outboundTunnel) matches(exePath string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.includedApps) == 0 {
		return true
	}
	if exePath == "" {
		return false
	}
	return t.includedApps[strings.ToLower(filepath.Clean(exePath))]
}

// updateIncludedApps swaps in a new app selection for an already-running
// tunnel — see the field doc on includedApps for why this exists. Only
// affects connections opened after the call; ones already relayed keep
// running on whichever path they started on (TCP can't migrate mid-flight
// regardless of what this app does).
func (t *outboundTunnel) updateIncludedApps(apps []string) {
	included := make(map[string]bool, len(apps))
	for _, p := range apps {
		included[strings.ToLower(filepath.Clean(p))] = true
	}
	t.mu.Lock()
	t.includedApps = included
	t.mu.Unlock()
}

// hasHandshake reports whether the WireGuard/AmneziaWG session has ever
// completed a handshake with its peer — i.e. whether the tunnel is
// actually passing traffic, not just "the local adapter/netstack came up
// with no error," which is all Bridge.Start previously guaranteed.
// Confirmed live 2026-09-04: a profile with a wrong endpoint/dead server
// still reported "Connected" in the UI while every single relayViaTunnel
// dial timed out — the local side has no way to know the tunnel is dead
// without asking the WireGuard device directly. Parses IpcGet()'s UAPI
// text output (the same interface `wg show` itself uses) rather than
// reaching into unexported peer fields.
func (t *outboundTunnel) hasHandshake() bool {
	uapi, err := t.dev.IpcGet()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(uapi, "\n") {
		sec, ok := strings.CutPrefix(line, "last_handshake_time_sec=")
		if !ok {
			continue
		}
		if sec != "" && sec != "0" {
			return true
		}
	}
	return false
}

func (t *outboundTunnel) dial(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	return t.net.DialContextTCPAddrPort(ctx, addr)
}

func (t *outboundTunnel) dialUDP(addr netip.AddrPort) (net.Conn, error) {
	return t.net.DialUDPAddrPort(netip.AddrPort{}, addr)
}
