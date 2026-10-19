// Bridge owns one running instance of the Wintun<->gVisor capture +
// per-app-routed relay — everything that used to be main()'s linear
// connect-then-block-on-Ctrl+C flow, now split into Start()/Stop() so it
// can be started and stopped repeatedly (by a future service's Connect/
// Disconnect RPCs) instead of running exactly once per process lifetime.
// Pure restructuring of the logic that lived directly in main.go — no
// behavior change; see ~/.claude/plans/idempotent-nibbling-hinton.md.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	adapterName = "Spectrune"
	tunnelType  = "Spectrune"
	mtu         = 1420
	nicID       = tcpip.NICID(1)
	fwdMaxInFlt = 1024
	bridgeAddr  = "10.99.0.1"
)

// procConvertInterfaceLuidToIndex resolves an interface's ifIndex from
// its LUID — modiphlpapi is already declared in getpid_windows.go, same
// package. netsh's "name=" parameter accepts a numeric index exactly
// like a friendly name, and unlike the name, the index is fixed at
// creation and never changes — which matters here specifically because
// the *name* Windows actually assigns can differ from adapterName (see
// Start's CreateAdapter fallback doc) and, worse, confirmed live
// 2026-09-14, isn't settled yet even microseconds after creation:
// ConvertInterfaceLuidToAlias queried right after CreateAdapter returned
// the pre-rename name ("Spectrune") while Get-NetAdapter moments later
// already showed the post-rename one ("Spectrune 1") — a race with no
// stable name to resolve at all until some indeterminate point after
// creation. The index has no such window.
var procConvertInterfaceLuidToIndex = modiphlpapi.NewProc("ConvertInterfaceLuidToIndex")

func adapterIndex(luid uint64) (uint32, error) {
	var index uint32
	ret, _, _ := procConvertInterfaceLuidToIndex.Call(
		uintptr(unsafe.Pointer(&luid)),
		uintptr(unsafe.Pointer(&index)),
	)
	if ret != 0 {
		return 0, fmt.Errorf("ConvertInterfaceLuidToIndex: error code %d", ret)
	}
	return index, nil
}

// adapterGUID is passed to wintun.CreateAdapter on every Start() instead of
// nil. A nil GUID tells Wintun to mint a brand-new random one each call, so
// every Start() created a genuinely new, distinct virtual adapter — Close()
// on a clean Stop() removes that one, but any Start() that ends via a
// force-killed process (exactly what the dev-loop deploy script's
// `Stop-Process -Force` does, and what a crash does too) skips Close()
// entirely and leaks it forever. Confirmed live 2026-09-07: 32 orphaned
// "Spectrune" adapters had piled up on the test machine. A fixed GUID makes
// every Start() address the *same* adapter identity, so Wintun replaces/
// reuses it in place instead of minting a new one — same fix WireGuard's
// own Windows client uses (a GUID derived from the tunnel name). This is
// just a random v4 UUID with no other significance; it must never change,
// or existing installs would leak one more adapter on their next update
// before settling.
var adapterGUID = windows.GUID{
	Data1: 0x1EFC71CF,
	Data2: 0xB95E,
	Data3: 0x40AB,
	Data4: [8]byte{0x8B, 0x25, 0xF3, 0x55, 0x76, 0xC0, 0x7B, 0x70},
}

// Bridge is one connect/disconnect cycle's worth of state. Not safe for
// concurrent Start/Stop calls — callers (main.go today, the future
// service's RPC handler later) are responsible for serializing them.
type Bridge struct {
	// realIfaceIndex is the physical/Wi-Fi interface Windows was using
	// BEFORE we install our own default route — sockets we dial ourselves
	// must be pinned to it (via IP_UNICAST_IF) or they'd loop back into
	// our own TUN.
	realIfaceIndex uint32

	// tunnels are the outbound AmneziaWG tunnels currently connected, in
	// connect order — one per active profile. Several can run at once
	// over the same Wintun adapter: each is a self-contained in-memory
	// netstack, so adding one is just another entry here, not another
	// adapter. pickTunnel decides which one (if any) carries a given
	// connection. Empty means pure pass-through (everything relays
	// direct). Guarded by tunMu: the forwarders read it on every
	// intercepted connection while Connect/Disconnect edit it.
	tunMu   sync.RWMutex
	tunnels []*namedTunnel

	// fullExtras records whether the full-tunnel-only extras (IPv6
	// default route + firewall block, tunnel DNS on the adapter) are
	// currently applied — see syncFullTunnelExtras.
	fullExtras bool

	adapter        *wintun.Adapter
	session        wintun.Session
	sessionStarted bool
	stack          *stack.Stack
	ep             *channel.Endpoint

	// ifaceAlias is the adapter's *actual* interface name, resolved from
	// its LUID right after creation — usually adapterName, but can
	// differ (see Start's doc comment on the CreateAdapter fallback), so
	// Stop's own netsh cleanup needs this rather than assuming the
	// constant.
	ifaceAlias string

	// closeFlag and pumpsDone coordinate a safe shutdown of the two pump
	// goroutines before the Wintun session/adapter get closed — see Stop.
	closeFlag atomic.Bool
	pumpsDone sync.WaitGroup

	readWait      windows.Handle
	stopStackPump context.CancelFunc

	pumpPacketCount int

	// ipv6RouteAdded records whether a ::/0 IPv6 default route via the
	// Wintun adapter is in place (full-tunnel mode only) — see
	// applyFullTunnelExtras/removeFullTunnelExtras.
	ipv6RouteAdded bool

	// conns tracks every in-flight relayed connection (both TCP and UDP,
	// both TUNNEL and direct) so UpdateIncludedApps can force-close the
	// ones whose path no longer matches right after an app-selection
	// change — an already-open TCP/UDP "connection" can't be migrated to
	// a different path, but closing the app-facing side makes the OS
	// report it dropped, and apps/browsers reconnect on their own. Without
	// this, a stale connection just sits there until it happens to close
	// naturally, which is what made editing Apps look like it needed a
	// full browser restart to take effect.
	connsMu    sync.Mutex
	nextConnID uint64
	conns      map[uint64]*trackedConn
}

type trackedConn struct {
	procName string
	dstIP    string
	tunnel   string // carrying tunnel's profile name, "" for direct
	closer   io.Closer
}

// namedTunnel is one connected profile's outbound tunnel.
type namedTunnel struct {
	name  string
	t     *outboundTunnel
	since time.Time // when it was added, for the GUI's connection timer
}

// startBridge brings a bridge up with name's tunnel (built from cfg) as
// its first one — or with none at all when cfg is nil (pure pass-through
// mode, everything relays direct, useful for testing the capture path
// alone). The tunnel is built BEFORE the adapter exists: once the
// Wintun default route is in place, bestOutboundInterface would answer
// with the Wintun adapter itself, and a tunnel that fails to come up
// (bad endpoint, ...) shouldn't have touched system routing at all.
func startBridge(name string, cfg *conf.Config) (*Bridge, error) {
	realIfaceIndex, err := bestOutboundInterface(netip.MustParseAddr("1.1.1.1"))
	if err != nil {
		return nil, fmt.Errorf("bestOutboundInterface: %w (are we even online yet?)", err)
	}
	log.Printf("real outbound interface index: %d", realIfaceIndex)

	var outTun *outboundTunnel
	if cfg != nil {
		if outTun, err = newOutboundTunnel(cfg, realIfaceIndex); err != nil {
			return nil, fmt.Errorf("newOutboundTunnel: %w", err)
		}
	} else {
		log.Printf("running in pure pass-through mode, everything relays direct")
	}

	b := &Bridge{realIfaceIndex: realIfaceIndex}
	if err := b.Start(); err != nil {
		if outTun != nil {
			outTun.close()
		}
		return nil, err
	}
	if outTun != nil {
		b.addTunnel(name, outTun)
	}
	return b, nil
}

// AddTunnel connects one more profile alongside the ones already running
// on this bridge — see the tunnels field doc.
func (b *Bridge) AddTunnel(name string, cfg *conf.Config) error {
	outTun, err := newOutboundTunnel(cfg, b.realIfaceIndex)
	if err != nil {
		return fmt.Errorf("newOutboundTunnel: %w", err)
	}
	b.addTunnel(name, outTun)
	return nil
}

func (b *Bridge) addTunnel(name string, outTun *outboundTunnel) {
	if outTun.isFullTunnel() {
		log.Printf("tunnel %q up, full-tunnel mode (no app selection, everything else routed)", name)
	} else {
		log.Printf("tunnel %q up, %d included app(s)", name, len(outTun.includedApps))
		for p := range outTun.includedApps {
			log.Printf("  includedApp: %q", p)
		}
	}
	b.tunMu.Lock()
	b.tunnels = append(b.tunnels, &namedTunnel{name: name, t: outTun, since: time.Now()})
	b.tunMu.Unlock()
	b.syncFullTunnelExtras()
	// Connections already open on another path that this tunnel now
	// claims get closed so the apps reconnect through it — same as
	// editing a connected profile's app list.
	b.closeStaleConns()
}

// RemoveTunnel disconnects one profile, leaving the bridge and any other
// tunnels running. Returns how many tunnels remain — the caller stops the
// whole bridge once that's 0.
func (b *Bridge) RemoveTunnel(name string) int {
	b.tunMu.Lock()
	var removed *outboundTunnel
	for i, nt := range b.tunnels {
		if nt.name == name {
			removed = nt.t
			b.tunnels = append(b.tunnels[:i:i], b.tunnels[i+1:]...)
			break
		}
	}
	left := len(b.tunnels)
	b.tunMu.Unlock()
	if removed == nil {
		return left
	}
	if left > 0 {
		b.syncFullTunnelExtras()
		// Everything that was relayed through the removed tunnel no
		// longer matches it, so this closes exactly those (and anything a
		// remaining tunnel now picks up instead).
		b.closeStaleConns()
	}
	removed.close()
	log.Printf("tunnel %q removed, %d still connected", name, left)
	return left
}

// TunnelNames returns the connected profiles' names, in connect order.
func (b *Bridge) TunnelNames() []string {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	names := make([]string, len(b.tunnels))
	for i, nt := range b.tunnels {
		names[i] = nt.name
	}
	return names
}

// tunnel returns name's tunnel, or nil if that profile isn't connected.
func (b *Bridge) tunnel(name string) *outboundTunnel {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	for _, nt := range b.tunnels {
		if nt.name == name {
			return nt.t
		}
	}
	return nil
}

// pickTunnel decides which connected tunnel carries a connection from
// procName to dstIP, or nil for direct. Split-tunnel profiles (with an
// app or domain selection) come first, in connect order — so when two of
// them claim the same app, the one connected first wins. A full-tunnel
// profile only gets what no split profile claimed, which is what makes
// "everything via A, but Telegram via B" work.
func (b *Bridge) pickTunnel(procName, dstIP string) *namedTunnel {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	var full *namedTunnel
	for _, nt := range b.tunnels {
		if nt.t.isFullTunnel() {
			if full == nil {
				full = nt
			}
			continue
		}
		if nt.t.matches(procName) || nt.t.matchesDomainIP(dstIP) {
			return nt
		}
	}
	return full
}

// observeDNSResponse hands a DNS response to every connected tunnel, each
// of which learns the IPs for its own domain lists.
func (b *Bridge) observeDNSResponse(payload []byte) {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	for _, nt := range b.tunnels {
		nt.t.observeDNSResponse(payload)
	}
}

// fullTunnelDNS returns the DNS servers of the first connected full-tunnel
// profile, or nil if none is connected.
func (b *Bridge) fullTunnelDNS() ([]netip.Addr, bool) {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	for _, nt := range b.tunnels {
		if nt.t.isFullTunnel() {
			return nt.t.dns, true
		}
	}
	return nil, false
}

// Start brings the bridge itself up: creates the Wintun adapter and
// gVisor netstack, wires the TCP/UDP forwarders, and makes the Wintun
// adapter the system default route. Tunnels are added separately (see
// startBridge/AddTunnel). b.realIfaceIndex must already be set. On any
// failure, whatever was already brought up is torn down before returning
// the error — unlike the old log.Fatalf call sites this replaces, a
// failed Start here does not exit the process, so it must not leak the
// adapter/session.
func (b *Bridge) Start() error {
	var err error

	// A stale adapter from an unclean previous shutdown (a crash, a
	// force-killed process, or the whole machine losing power/being
	// reset mid-connection — none of which run Stop()'s normal Close())
	// leaves the fixed-GUID adapter still registered at the driver level
	// even though nothing is using it, and CreateAdapter below fails
	// outright against it ("Cannot create a file when that file already
	// exists") rather than reusing it — confirmed live 2026-09-14, and
	// previously worked around by rebooting the whole machine, which is
	// not a reasonable thing to ask a user to do.
	//
	// Also confirmed live the same day: a "failed" CreateAdapter call
	// can still leave a real, working device behind at the OS level
	// despite returning an error to us (its log even names a specific
	// sub-step, "Failed to initiate stub device creation" — the device
	// itself was already created by an earlier step). That device then
	// collides with the *next* retry attempt exactly like a genuinely
	// stale one would, so cleanup has to run before every attempt, not
	// just once up front — otherwise each retry just compounds the mess
	// (confirmed: chasing this the naive way left three live orphaned
	// adapters — "Spectrune", "Spectrune 1", and a nil-GUID one — after
	// one bad connect attempt).
	var adapter *wintun.Adapter
	for attempt := 1; attempt <= 4; attempt++ {
		if stale, err := wintun.OpenAdapter(adapterName); err == nil {
			log.Printf("attempt %d/4: found an existing %q adapter (stale, or left behind by the previous attempt), removing it", attempt, adapterName)
			stale.Close()
			// PnP device removal is asynchronous — confirmed live
			// 2026-09-14 that CreateAdapter run immediately after
			// Close() still fails with the same ERROR_ALREADY_EXISTS
			// (0x800700B7) because Windows hadn't actually finished
			// tearing down the old device yet.
			time.Sleep(500 * time.Millisecond)
		}
		adapter, err = wintun.CreateAdapter(adapterName, tunnelType, &adapterGUID)
		if err == nil {
			break
		}
		log.Printf("CreateAdapter attempt %d/4 failed: %v", attempt, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		// Give up on the fixed identity and let Windows mint a fresh
		// random one (nil GUID) — better than leaving the user stuck
		// requiring a manual pnputil/Device-Manager cleanup. The only
		// real downside (per adapterGUID's own doc comment) is one
		// extra Windows network-profile (NLA) entry, trivial next to
		// "VPN refuses to connect at all." One last OpenAdapter+Close
		// first, for the same reason as inside the loop above.
		log.Printf("CreateAdapter still failing after retries, falling back to a fresh random adapter identity: %v", err)
		if stale, oerr := wintun.OpenAdapter(adapterName); oerr == nil {
			stale.Close()
			time.Sleep(500 * time.Millisecond)
		}
		adapter, err = wintun.CreateAdapter(adapterName, tunnelType, nil)
	}
	if err != nil {
		return b.failStart(fmt.Errorf("CreateAdapter: %w", err))
	}
	b.adapter = adapter
	log.Printf("adapter created, LUID=%d", adapter.LUID())

	session, err := adapter.StartSession(0x800000)
	if err != nil {
		return b.failStart(fmt.Errorf("StartSession: %w", err))
	}
	b.session = session
	b.sessionStarted = true

	ep := channel.New(1024, uint32(mtu), "")
	b.ep = ep
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		HandleLocal:        false,
	})
	b.stack = s
	if tcpipErr := s.CreateNIC(nicID, ep); tcpipErr != nil {
		return b.failStart(fmt.Errorf("CreateNIC: %v", tcpipErr))
	}
	// Without these, gVisor silently drops any unicast packet whose
	// destination isn't an address the stack itself owns — exactly what
	// we need for transparent interception of arbitrary destinations.
	// This was the actual bug behind "packets reach Wintun (confirmed via
	// NDIS adapter counters) but the TCP forwarder never fires."
	if tcpipErr := s.SetPromiscuousMode(nicID, true); tcpipErr != nil {
		return b.failStart(fmt.Errorf("SetPromiscuousMode: %v", tcpipErr))
	}
	if tcpipErr := s.SetSpoofing(nicID, true); tcpipErr != nil {
		return b.failStart(fmt.Errorf("SetSpoofing: %v", tcpipErr))
	}

	testAddr := netip.MustParseAddr(bridgeAddr)
	protoAddr := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFromSlice(testAddr.AsSlice()).WithPrefix(),
	}
	if tcpipErr := s.AddProtocolAddress(nicID, protoAddr, stack.AddressProperties{}); tcpipErr != nil {
		return b.failStart(fmt.Errorf("AddProtocolAddress: %v", tcpipErr))
	}
	s.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: nicID})

	fwd := tcp.NewForwarder(s, 0, fwdMaxInFlt, b.handleForwarded)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)

	udpFwd := udp.NewForwarder(s, b.handleUDPForwarded)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	b.readWait = session.ReadWaitEvent()
	b.pumpsDone.Add(2)
	go b.pumpWintunToStack()
	stackCtx, stopStackPump := context.WithCancel(context.Background())
	b.stopStackPump = stopStackPump
	go b.pumpStackToWintun(stackCtx)

	log.Printf("bridge up, gVisor address %s", testAddr)

	// The adapter's actual interface name can differ from adapterName —
	// confirmed live 2026-09-14: when the CreateAdapter fallback above
	// (nil GUID) runs because a stale ghost device is still squatting on
	// the *name* "Spectrune" too, not just its GUID, Windows renames the
	// newly-created one (e.g. "Spectrune 1") to avoid that collision —
	// and not even immediately: querying the name right after creation
	// raced the rename and got the stale pre-rename answer. The index is
	// fixed at creation with no such window, and netsh's "name=" accepts
	// one exactly like a friendly name, so resolving that instead
	// sidesteps the whole race.
	ifIndex, err := adapterIndex(adapter.LUID())
	alias := adapterName
	if err != nil {
		log.Printf("adapterIndex: %v, falling back to %q", err, adapterName)
	} else {
		alias = strconv.FormatUint(uint64(ifIndex), 10)
	}
	b.ifaceAlias = alias

	if err := configureWindowsInterface(alias); err != nil {
		// The pumps are already running at this point — go through the
		// same teardown Stop() uses rather than a partial failStart, so
		// they're stopped cleanly before the session/adapter close.
		b.Stop()
		return fmt.Errorf("configureWindowsInterface: %w", err)
	}

	log.Printf("Bridge active.")
	return nil
}

// syncFullTunnelExtras applies or removes the full-tunnel-only extras
// (see applyFullTunnelExtras) to match whether any connected profile is
// a full-tunnel one right now. Called after every tunnel add/remove and
// after a connected profile's app selection is edited (which can turn it
// from split into full or back).
func (b *Bridge) syncFullTunnelExtras() {
	dns, needed := b.fullTunnelDNS()
	b.tunMu.Lock()
	applied := b.fullExtras
	b.fullExtras = needed
	b.tunMu.Unlock()
	switch {
	case needed && !applied:
		b.applyFullTunnelExtras(dns)
	case !needed && applied:
		b.removeFullTunnelExtras()
	}
}

// applyFullTunnelExtras sets up what only a full-tunnel profile needs.
//
// IPv6 leak guard: configureWindowsInterface only points the IPv4
// default route at the Wintun adapter, so a network with real IPv6
// connectivity keeps routing IPv6 straight out the physical adapter —
// never reaching this process's capture/relay logic at all, matched
// or not. Confirmed live 2026-09-03: Edge showed the real (non-VPN)
// IP on an IPv6-capable checker even though its IPv4 traffic was
// correctly matched and tunneled. A per-adapter NetAdapterBinding
// disable was tried first but wasn't reliable enough — traffic can
// leak via a tunnel/pseudo adapter (Teredo, 6to4, a VPN's own virtual
// NIC) that Get-NetAdapter/bestOutboundInterface never singles out. A
// blanket outbound firewall block for the entire IPv6 address range
// is authoritative regardless of which interface would've carried it.
//
// Only applied in full-tunnel mode (no app selection) — "route
// everything" is the only case this leak actually contradicts user
// intent. In split-tunnel mode the user has already said "just these
// apps go through the tunnel, everything else stays direct," so an
// app that was never included going out over IPv6 isn't a leak at
// all, it's exactly what was asked for — but the blanket block
// doesn't know the difference and kills IPv6 for literally every
// other process on the machine too. Confirmed live 2026-09-12: this
// broke an unrelated AnyDesk session (AnyDesk prefers IPv6 when
// available) on a split-tunnel profile that never included it.
// Best-effort — a failure here shouldn't block an otherwise-working
// connection.
func (b *Bridge) applyFullTunnelExtras(tunDNS []netip.Addr) {
	log.Printf("full-tunnel mode: blocking outbound IPv6 to prevent it bypassing the tunnel")

	// Add IPv6 default route via Wintun SYNCHRONOUSLY — a plain netsh
	// call takes milliseconds, unlike the WFP firewall block below.
	// Without this, Windows routes global IPv6 traffic through the
	// physical adapter (bypassing our Wintun bridge entirely) until the
	// WFP rule fires ~10 s later. During that window a browser's IPv6
	// TCP/TLS attempt to a DPI-blocked site (e.g. 2ip.io blocked by РКН)
	// results in a mid-handshake RST — reported as ERR_SSL_PROTOCOL_ERROR
	// — because the TLS ClientHello exits unencrypted through the real
	// adapter before the VPN tunnel can carry it. Once in place, all
	// IPv6 SYNs enter gVisor and are RST'd immediately by handleForwarded
	// (WireGuard is IPv4-only), so Chrome's Happy Eyeballs retries on
	// IPv4 via the tunnel instead of exposing the SNI to DPI.
	if out, err := exec.Command("netsh", "interface", "ipv6", "add", "route",
		"::/0", b.ifaceAlias, "metric=1").CombinedOutput(); err != nil {
		log.Printf("warning: could not add IPv6 default route via Wintun: %v (%s)", err, out)
	} else {
		log.Printf("IPv6 default route via Wintun added (adapter %s)", b.ifaceAlias)
		b.ipv6RouteAdded = true
	}

	// WFP firewall block is kept as belt-and-suspenders for Teredo/6to4
	// pseudo-adapters that tunnel IPv6 inside IPv4 UDP and bypass the
	// ::/0 route entirely (Get-NetAdapter never singles them out, so only
	// a blanket outbound WFP rule covers them reliably).
	// Genuinely best-effort (per the doc above) — run it in the
	// background rather than blocking Start()/the Connect RPC on it.
	// runPowerShellWithRetry can now take several seconds on a slow
	// or WMI-flaky machine (confirmed live 2026-09-15: retries here
	// made a real disconnect feel frozen for several seconds — see
	// unblockIPv6Firewall's call in Stop() for the matching fix),
	// and nothing about the tunnel actually depends on this
	// finishing before Connect replies.
	setIPv6Block(true, b.realIfaceIndex)

	// Configure DNS on the Wintun adapter so Windows stops using the
	// physical adapter's DNS (often a router IPv6 link-local like
	// fe80::..., unreachable through the WireGuard tunnel) and uses a
	// real resolver that flows through the tunnel instead. Without
	// this, all hostname resolution fails in full-tunnel mode on
	// machines where the router only advertises an IPv6 link-local
	// DNS server (common on modern home routers with SLAAC).
	if len(tunDNS) == 0 {
		tunDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	configureWintunDNS(b.ifaceAlias, tunDNS)
}

// removeFullTunnelExtras undoes applyFullTunnelExtras while the bridge
// itself keeps running (the last full-tunnel profile disconnected, split
// ones remain). Stop does the same cleanup on its own for a full
// teardown.
func (b *Bridge) removeFullTunnelExtras() {
	log.Printf("no full-tunnel profile connected any more: restoring IPv6 and adapter DNS")
	if b.ipv6RouteAdded {
		exec.Command("netsh", "interface", "ipv6", "delete", "route", "::/0", b.ifaceAlias).Run()
		b.ipv6RouteAdded = false
	}
	resetWintunDNS(b.ifaceAlias)
	setIPv6Block(false, b.realIfaceIndex)
}

// failStart tears down whatever partial state Start already created
// (adapter/session, if any) before returning err — called only for
// failures before the pumps start, so there's nothing else to stop.
func (b *Bridge) failStart(err error) error {
	if b.sessionStarted {
		b.session.End()
		b.sessionStarted = false
	}
	if b.adapter != nil {
		b.adapter.Close()
		b.adapter = nil
	}
	return err
}

// UpdateTunnel swaps the live app and domain selection of name's
// already-running tunnel — see Service.SaveProfile, which calls this when
// the profile being saved is connected. No-op if it isn't. Force-closes
// any already-open connection whose path no longer matches the new
// selection (see the conns field doc) so apps pick up the change without
// needing a full restart.
func (b *Bridge) UpdateTunnel(name string, apps, domains []string) {
	t := b.tunnel(name)
	if t == nil {
		return
	}
	t.updateIncludedApps(apps)
	t.updateDomains(domains)
	b.syncFullTunnelExtras()
	b.closeStaleConns()
}

// UpdateDomains swaps only the domain selection of name's running tunnel
// — see Service.SaveDomainList.
func (b *Bridge) UpdateDomains(name string, domains []string) {
	if t := b.tunnel(name); t != nil {
		t.updateDomains(domains)
	}
}

// TunnelState is one connected profile's status, as reported to the GUI.
type TunnelState struct {
	Name string
	// HandshakeOK — see outboundTunnel.hasHandshake's doc.
	HandshakeOK bool
	// RxBytes/TxBytes are the tunnel's traffic totals and Since its
	// connect time (unix seconds) — the GUI's speed and timer readouts.
	RxBytes, TxBytes uint64
	Since            int64
}

// TunnelStates reports every connected tunnel's handshake status, in
// connect order. The IpcGet queries run outside tunMu.
func (b *Bridge) TunnelStates() []TunnelState {
	b.tunMu.RLock()
	tunnels := append([]*namedTunnel(nil), b.tunnels...)
	b.tunMu.RUnlock()
	states := make([]TunnelState, len(tunnels))
	for i, nt := range tunnels {
		ok, rx, tx := nt.t.stats()
		states[i] = TunnelState{Name: nt.name, HandshakeOK: ok, RxBytes: rx, TxBytes: tx, Since: nt.since.Unix()}
	}
	return states
}

// appClaimedBy returns one of apps that a connected profile other than
// except already routes, and that profile's name ("" if none) — one app
// goes through one tunnel at a time, so the GUI hides those apps from
// other profiles' pickers and the service refuses the overlap.
func (b *Bridge) appClaimedBy(apps []string, except string) (app, profile string) {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	for _, nt := range b.tunnels {
		if nt.name == except || nt.t.isFullTunnel() {
			continue
		}
		if a := nt.t.sharedApp(apps); a != "" {
			return a, nt.name
		}
	}
	return "", ""
}

// tunnelPrivateKeyInUse reports which connected profile (if any) already
// uses key — a WireGuard server can't serve two simultaneous sessions
// for one key, so a second profile with it would silently break both.
func (b *Bridge) tunnelPrivateKeyInUse(key conf.Key) string {
	b.tunMu.RLock()
	defer b.tunMu.RUnlock()
	for _, nt := range b.tunnels {
		if nt.t.privateKey == key {
			return nt.name
		}
	}
	return ""
}

// trackConn registers a just-opened relayed connection and returns an id
// to hand back to untrackConn once it finishes.
func (b *Bridge) trackConn(procName, dstIP, tunnel string, c io.Closer) uint64 {
	b.connsMu.Lock()
	defer b.connsMu.Unlock()
	if b.conns == nil {
		b.conns = make(map[uint64]*trackedConn)
	}
	b.nextConnID++
	id := b.nextConnID
	b.conns[id] = &trackedConn{procName: procName, dstIP: dstIP, tunnel: tunnel, closer: c}
	return id
}

func (b *Bridge) untrackConn(id uint64) {
	b.connsMu.Lock()
	delete(b.conns, id)
	b.connsMu.Unlock()
}

// closeStaleConns force-closes every tracked connection whose recorded
// path (direct, or which tunnel) disagrees with what pickTunnel would
// decide right now — called right after an app-selection update or a
// tunnel being added/removed. Closing here
// only closes the app-facing side; the relay goroutine's own deferred
// cleanup tears down the remote leg and untracks it as it unwinds.
func (b *Bridge) closeStaleConns() {
	b.connsMu.Lock()
	var stale []io.Closer
	for _, c := range b.conns {
		want := ""
		if nt := b.pickTunnel(c.procName, c.dstIP); nt != nil {
			want = nt.name
		}
		if want != c.tunnel {
			stale = append(stale, c.closer)
		}
	}
	b.connsMu.Unlock()
	for _, c := range stale {
		c.Close()
	}
	if len(stale) > 0 {
		log.Printf("closed %d stale connection(s) after a routing change", len(stale))
	}
}

// Stop tears the bridge down: removes the default route, stops both pump
// goroutines, and closes the Wintun session/adapter. Safe to call once per
// successful Start.
func (b *Bridge) Stop() {
	log.Printf("stopping bridge, removing default route...")
	deleteRouteAlias := b.ifaceAlias
	if deleteRouteAlias == "" {
		deleteRouteAlias = adapterName
	}
	exec.Command("netsh", "interface", "ipv4", "delete", "route", "0.0.0.0/0", deleteRouteAlias).Run()
	if b.ipv6RouteAdded {
		exec.Command("netsh", "interface", "ipv6", "delete", "route", "::/0", deleteRouteAlias).Run()
	}
	resetWintunDNS(deleteRouteAlias)
	// Best-effort cleanup, run in the background rather than blocking
	// Stop()/the Disconnect RPC on it — same reasoning as blockIPv6Firewall's
	// call in Start(). This one matters even more in practice: it runs on
	// literally every disconnect (not just full-tunnel connects), and
	// runPowerShellWithRetry's retries made a real disconnect feel frozen
	// for several seconds on a slow/WMI-flaky machine — reported live
	// 2026-09-15 ("нажал кнопку отключить и он зависает"). If the whole
	// process exits before this finishes, the next launch's startup
	// safety net (service_windows.go) removes any rule left behind.
	setIPv6Block(false, b.realIfaceIndex)

	// Stop both pump goroutines and WAIT for them to actually return
	// before touching the session/adapter — closing either while a pump
	// is mid-syscall against it segfaults (this crashed the first version
	// of this shutdown path: ACCESS_VIOLATION inside wintun's
	// ReceivePacket, racing with adapter.Close() on another goroutine).
	// Same pattern amneziawg-go's own tun_windows.go Close() uses.
	b.closeFlag.Store(true)
	if b.readWait != 0 {
		windows.SetEvent(b.readWait)
	}
	if b.stopStackPump != nil {
		b.stopStackPump()
	}
	b.pumpsDone.Wait()
	if b.sessionStarted {
		b.session.End()
	}
	if b.adapter != nil {
		b.adapter.Close()
	}
	b.tunMu.Lock()
	tunnels := b.tunnels
	b.tunnels = nil
	b.tunMu.Unlock()
	for _, nt := range tunnels {
		nt.t.close()
	}
}

// configureWindowsInterface assigns bridgeAddr to the real Windows adapter
// and makes it the system default route. Uses netsh (simple, reliable,
// same tool used to debug this by hand) rather than raw IP Helper route
// APIs — fine for this prototype stage.
// ipv6FirewallRuleName is shared with service.go's startup safety net,
// which removes any leftover rule from an unclean shutdown.
const ipv6FirewallRuleName = "SpectruneBlockIPv6"

// runPowerShellWithRetry runs a PowerShell script up to 3 times (a first
// try, then two retries 1.5s apart) before giving up, returning the last
// attempt's error/output. The NetSecurity module's cmdlets (New-/Remove-
// NetFirewallRule) go through the MSFT_NetFirewallRule CIM/WMI provider,
// which depends on Windows' own Base Filtering Engine/WMI services —
// SpectruneService auto-starts and can race ahead of those being fully
// ready, especially right after boot. Confirmed live 2026-09-14: manually
// re-running the *exact* same script (both as a normal user and as
// SYSTEM via a Scheduled Task, well after boot) always succeeded
// immediately, while the real service's own calls intermittently failed
// with a bare "exit status 1" and empty output — a transient race, not a
// bad command, is the only explanation consistent with both observations.
func runPowerShellWithRetry(script string) (string, error) {
	var out []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		out, err = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
		if err == nil {
			return string(out), nil
		}
		if attempt < 3 {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	return string(out), err
}

// ipv6BlockWant/ipv6BlockMu order the background IPv6 firewall block/
// unblock runs (setIPv6Block). Each takes several seconds of PowerShell,
// so a full-tunnel profile disconnected right after connecting used to
// have its unblock finish BEFORE the still-running block, leaving IPv6
// blocked machine-wide with only split profiles (or nothing) connected —
// found live 2026-10-18 testing several simultaneous profiles.
var (
	ipv6BlockMu   sync.Mutex
	ipv6BlockWant atomic.Bool
)

// setIPv6Block applies (want) or removes the outbound IPv6 block in the
// background. A run that a later call has already overridden is skipped,
// so whatever was asked for last is what ends up in place.
func setIPv6Block(want bool, ifaceIndex uint32) {
	ipv6BlockWant.Store(want)
	go func() {
		ipv6BlockMu.Lock()
		defer ipv6BlockMu.Unlock()
		if ipv6BlockWant.Load() != want {
			return
		}
		if !want {
			if err := unblockIPv6Firewall(ifaceIndex); err != nil {
				log.Printf("warning: could not remove outbound IPv6 block: %v", err)
			}
			return
		}
		if err := blockIPv6Firewall(ifaceIndex); err != nil {
			log.Printf("warning: could not block outbound IPv6 (possible IPv6 leak): %v", err)
		} else {
			log.Printf("outbound IPv6 blocked successfully")
		}
	}()
}

// blockIPv6Firewall blocks all outbound IPv6 on the real outbound interface.
// First tries a Windows Firewall rule; if that fails (HRESULT 0x80070057 or
// similar — observed on some Win11 machines regardless of parameters), falls
// back to disabling the ms_tcpip6 binding on that specific adapter, which
// works unconditionally and does not depend on the Firewall rule API.
func blockIPv6Firewall(ifaceIndex uint32) error {
	script := fmt.Sprintf(
		`Remove-NetFirewallRule -DisplayName '%[1]s' -ErrorAction SilentlyContinue; `+
			`New-NetFirewallRule -DisplayName '%[1]s' -Direction Outbound -Action Block -AddressFamily IPv6 -Profile Any`,
		ipv6FirewallRuleName,
	)
	if _, err := runPowerShellWithRetry(script); err == nil {
		return nil
	}
	// Firewall rule failed — disable IPv6 binding on the real adapter instead.
	script2 := fmt.Sprintf(
		`Get-NetAdapter | Where-Object { $_.InterfaceIndex -eq %d } | Disable-NetAdapterBinding -ComponentID ms_tcpip6 -ErrorAction Stop; exit 0`,
		ifaceIndex,
	)
	out, err := runPowerShellWithRetry(script2)
	if err != nil {
		return fmt.Errorf("Disable-NetAdapterBinding: %w (%s)", err, out)
	}
	return nil
}

// unblockIPv6Firewall undoes whatever blockIPv6Firewall did: removes the
// firewall rule if present, and re-enables the ms_tcpip6 binding on the
// real adapter if it was disabled. Both cleanups are best-effort and silent
// so this is safe to call even when only one method was used.
func unblockIPv6Firewall(ifaceIndex uint32) error {
	script := fmt.Sprintf(
		`$r = Get-NetFirewallRule -DisplayName '%[1]s' -ErrorAction SilentlyContinue; if ($r) { $r | Remove-NetFirewallRule -ErrorAction Stop }; `+
			`Get-NetAdapter | Where-Object { $_.InterfaceIndex -eq %[2]d } | Enable-NetAdapterBinding -ComponentID ms_tcpip6 -ErrorAction SilentlyContinue; `+
			`exit 0`,
		ipv6FirewallRuleName, ifaceIndex,
	)
	out, err := runPowerShellWithRetry(script)
	if err != nil {
		return fmt.Errorf("Remove-NetFirewallRule: %w (%s)", err, out)
	}
	return nil
}

func configureWindowsInterface(alias string) error {
	cmds := [][]string{
		{"netsh", "interface", "ipv4", "set", "address", "name=" + alias, "static", bridgeAddr, "255.255.255.0"},
		{"netsh", "interface", "ipv4", "add", "route", "0.0.0.0/0", alias, "metric=1"},
	}
	for _, c := range cmds {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		log.Printf("$ %v -> %s", c, string(out))
		if err != nil {
			return err
		}
	}
	return nil
}

// configureWintunDNS sets the DNS servers on the Wintun adapter so that
// Windows uses them instead of whatever the physical adapter advertised
// (often a router's IPv6 link-local address, which is unreachable through
// the WireGuard tunnel and causes all DNS — and therefore all HTTPS — to
// fail in full-tunnel mode). Only called in full-tunnel mode.
func configureWintunDNS(alias string, dns []netip.Addr) {
	if len(dns) == 0 {
		return
	}
	// Set primary; netsh replaces whatever was there.
	if out, err := exec.Command("netsh", "interface", "ipv4", "set", "dns",
		"name="+alias, "static", dns[0].String(), "primary").CombinedOutput(); err != nil {
		log.Printf("warning: set DNS primary %s: %v (%s)", dns[0], err, out)
		return
	}
	for i, d := range dns[1:] {
		exec.Command("netsh", "interface", "ipv4", "add", "dns",
			"name="+alias, d.String(), fmt.Sprintf("index=%d", i+2)).Run()
	}
	log.Printf("full-tunnel DNS configured on adapter %s: %v", alias, dns)
}

// resetWintunDNS restores DHCP-assigned DNS on the Wintun adapter.
func resetWintunDNS(alias string) {
	exec.Command("netsh", "interface", "ipv4", "set", "dns", "name="+alias, "dhcp").Run()
}

// removeIPv6FirewallRule removes the SpectruneBlockIPv6 firewall rule if it
// exists — used by the startup safety net in service_windows.go, which runs
// before a real connect so no ifaceIndex is available. Does not touch adapter
// bindings (those are restored by unblockIPv6Firewall on a normal disconnect).
func removeIPv6FirewallRule() error {
	script := fmt.Sprintf("$r = Get-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue; if ($r) { $r | Remove-NetFirewallRule -ErrorAction Stop }; exit 0", ipv6FirewallRuleName)
	out, err := runPowerShellWithRetry(script)
	if err != nil {
		return fmt.Errorf("Remove-NetFirewallRule: %w (%s)", err, out)
	}
	return nil
}

// handleForwarded/handleUDPForwarded both special-case pid == os.Getpid()
// — this daemon process's *own* outbound traffic (the update checker's
// GitHub API calls, in particular) always goes direct, never through
// its own tunnel, regardless of full-tunnel/split-tunnel/domain-list
// matching. Confirmed live 2026-09-13: on a full-tunnel profile, the
// daemon's own "check for updates" HTTP client got swept into its own
// interception and relayed via the VPN peer, and a slow/distant peer
// blew past its 10s timeout, logging a spurious-looking "context
// deadline exceeded" that had nothing to do with GitHub. There's no
// scenario where routing the app's own maintenance traffic through the
// tunnel it itself manages is desirable — worst case it makes the VPN
// depend on itself to fetch its own fix.
func (b *Bridge) handleForwarded(r *tcp.ForwarderRequest) {
	id := r.ID()
	// WireGuard tunnel is IPv4-only. If we accept an IPv6 SYN (which gVisor
	// does because the NIC is in promiscuous+spoofing mode), we complete the
	// three-way handshake and then fail the relay — the browser has already
	// committed to the IPv6 path and sees a reset mid-TLS handshake, which
	// Chrome reports as ERR_SSL_PROTOCOL_ERROR rather than a connection
	// error. Refuse immediately instead: Chrome's Happy Eyeballs then falls
	// back to IPv4 without delay. This also covers the ~10s window between
	// Bridge.Start and blockIPv6Firewall finishing in the background.
	if id.LocalAddress.Len() != 4 {
		r.Complete(true) // RST
		return
	}
	appAddr := net.JoinHostPort(id.RemoteAddress.String(), portStr(id.RemotePort))
	dstAddr := net.JoinHostPort(id.LocalAddress.String(), portStr(id.LocalPort))

	pid, perr := findTCPOwnerPID(id.RemotePort)
	procName := "?"
	if perr == nil {
		if p, err := processPath(pid); err == nil {
			procName = p
		}
	}
	log.Printf("TCP %s -> %s (pid=%d exe=%q)", appAddr, dstAddr, pid, procName)

	var wq waiter.Queue
	endpoint, err := r.CreateEndpoint(&wq)
	if err != nil {
		log.Printf("  CreateEndpoint failed: %v", err)
		r.Complete(true)
		return
	}
	r.Complete(false)
	local := gonet.NewTCPConn(&wq, endpoint)

	var nt *namedTunnel
	if int(pid) != os.Getpid() {
		nt = b.pickTunnel(procName, id.LocalAddress.String())
	}
	if nt != nil {
		log.Printf("  -> TUNNEL %q (matched %s)", nt.name, procName)
		go b.relayViaTunnel(nt, local, id.LocalAddress.String(), id.LocalPort, procName)
	} else {
		log.Printf("  -> direct")
		go b.relayDirect(local, dstAddr, id.LocalPort, procName)
	}
}

const udpIdleTimeout = 30 * time.Second

func (b *Bridge) handleUDPForwarded(r *udp.ForwarderRequest) {
	id := r.ID()
	// IPv6 UDP is silently dropped — same reasoning as handleForwarded's
	// IPv6 RST: the WireGuard tunnel is IPv4-only and relay would fail.
	if id.LocalAddress.Len() != 4 {
		return
	}
	appAddr := net.JoinHostPort(id.RemoteAddress.String(), portStr(id.RemotePort))
	dstAddr := net.JoinHostPort(id.LocalAddress.String(), portStr(id.LocalPort))

	pid, perr := findUDPOwnerPID(id.RemotePort)
	procName := "?"
	if perr == nil {
		if p, err := processPath(pid); err == nil {
			procName = p
		}
	}

	var wq waiter.Queue
	endpoint, tcpipErr := r.CreateEndpoint(&wq)
	if tcpipErr != nil {
		log.Printf("UDP %s -> %s: CreateEndpoint failed: %v", appAddr, dstAddr, tcpipErr)
		return
	}
	local := gonet.NewUDPConn(&wq, endpoint)

	// DNS responses are snooped regardless of which path carries them —
	// an app that isn't itself included can still resolve a domain that's
	// in an enabled domain list, and the resulting IP needs to be learned
	// either way for matchesDomainIP to catch that app's *next* connection
	// (the actual HTTPS/etc traffic, a separate connection from the DNS
	// lookup itself).
	var onResponse func([]byte)
	if id.LocalPort == 53 {
		onResponse = b.observeDNSResponse
	}

	var nt *namedTunnel
	if int(pid) != os.Getpid() {
		nt = b.pickTunnel(procName, id.LocalAddress.String())
	}
	if nt != nil {
		log.Printf("UDP %s -> %s (pid=%d exe=%q) -> TUNNEL %q", appAddr, dstAddr, pid, procName, nt.name)
		go b.relayUDP(local, procName, id.LocalAddress.String(), nt.name, func() (net.Conn, error) {
			addr, err := netip.ParseAddr(id.LocalAddress.String())
			if err != nil {
				return nil, err
			}
			return nt.t.dialUDP(netip.AddrPortFrom(addr, id.LocalPort))
		}, onResponse)
	} else {
		go b.relayUDP(local, procName, id.LocalAddress.String(), "", func() (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second, Control: b.bindToRealIface}
			return d.Dial("udp4", dstAddr)
		}, onResponse)
	}
}

// relayUDP pipes datagrams both ways between the intercepted local UDP
// "connection" (already bound to the app's address, per gVisor's UDP
// forwarder) and a freshly dialed remote socket, closing both sides after
// a period of inactivity — UDP has no close handshake to key off of.
//
// onResponse, when non-nil, is handed a copy of every remote->local
// datagram before it's forwarded — used only for DNS connections
// (destination port 53), to passively learn domain->IP associations for
// domain-list routing (see outboundTunnel.observeDNSResponse). Called
// regardless of which path carries it: an app that isn't itself included can still
// resolve a domain that's in an enabled list, and that resolution needs
// to be observed no matter which path carried the query.
func (b *Bridge) relayUDP(local *gonet.UDPConn, procName, dstIP, tunnel string, dial func() (net.Conn, error), onResponse func([]byte)) {
	connID := b.trackConn(procName, dstIP, tunnel, local)
	defer b.untrackConn(connID)
	defer local.Close()
	remote, err := dial()
	if err != nil {
		log.Printf("  relayUDP: dial failed: %v", err)
		return
	}
	defer remote.Close()

	pipe := func(dst, src net.Conn, snoop func([]byte)) {
		buf := make([]byte, 65535)
		for {
			src.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := src.Read(buf)
			if err != nil {
				return
			}
			if snoop != nil {
				snoop(buf[:n])
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
	done := make(chan struct{}, 2)
	go func() { pipe(remote, local, nil); done <- struct{}{} }()
	go func() { pipe(local, remote, onResponse); done <- struct{}{} }()
	<-done
}

func (b *Bridge) bindToRealIface(_, _ string, c syscall.RawConn) error {
	var ctrlErr error
	err := c.Control(func(fd uintptr) {
		ctrlErr = bindSocketToInterface4(windows.Handle(fd), b.realIfaceIndex)
	})
	if err != nil {
		return err
	}
	return ctrlErr
}

// dnsTCPSink accumulates a stream of TCP/53 bytes and hands each
// complete, length-prefixed DNS message (RFC 1035 §4.2.2 framing: a
// 2-byte big-endian length before each message) to onMessage as it
// completes. A TCP DNS response can arrive split across multiple reads,
// or with more than one message back-to-back in a single read — unlike
// UDP, where relayUDP's per-datagram snoop already lines up with
// "one read = one message." Implements io.Writer so it can sit behind
// an io.TeeReader on the remote->local copy without touching the
// existing streaming relay logic.
type dnsTCPSink struct {
	buf       []byte
	onMessage func([]byte)
}

func (s *dnsTCPSink) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for len(s.buf) >= 2 {
		msgLen := int(s.buf[0])<<8 | int(s.buf[1])
		if len(s.buf) < 2+msgLen {
			break
		}
		s.onMessage(s.buf[2 : 2+msgLen])
		s.buf = s.buf[2+msgLen:]
	}
	// Cap unbounded growth from a non-DNS TCP/53 talker (shouldn't
	// happen — port 53 is always DNS in practice — but don't let a
	// malformed stream grow this forever).
	if len(s.buf) > 65535 {
		s.buf = nil
	}
	return len(p), nil
}

func (b *Bridge) relayViaTunnel(nt *namedTunnel, local *gonet.TCPConn, dstIP string, dstPort uint16, procName string) {
	connID := b.trackConn(procName, dstIP, nt.name, local)
	defer b.untrackConn(connID)
	defer local.Close()
	addr, err := netip.ParseAddr(dstIP)
	if err != nil {
		log.Printf("  relayViaTunnel: bad addr %s: %v", dstIP, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remote, err := nt.t.dial(ctx, netip.AddrPortFrom(addr, dstPort))
	if err != nil {
		log.Printf("  relayViaTunnel: dial %s:%d via tunnel FAILED: %v", dstIP, dstPort, err)
		return
	}
	log.Printf("  relayViaTunnel: dial %s:%d via tunnel OK, relaying", dstIP, dstPort)
	defer remote.Close()

	var src io.Reader = remote
	if dstPort == 53 {
		src = io.TeeReader(remote, &dnsTCPSink{onMessage: b.observeDNSResponse})
	}

	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, src); done <- struct{}{} }()
	<-done
}

// relayDirect dials the real destination directly, over the real physical
// interface (bypassing our own default route via IP_UNICAST_IF), and
// pipes bytes both ways. This is the "not included" path.
func (b *Bridge) relayDirect(local *gonet.TCPConn, dstAddr string, dstPort uint16, procName string) {
	dstIP, _, _ := net.SplitHostPort(dstAddr)
	connID := b.trackConn(procName, dstIP, "", local)
	defer b.untrackConn(connID)
	defer local.Close()
	log.Printf("  relayDirect: dialing %s via iface %d", dstAddr, b.realIfaceIndex)

	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, _ string, c syscall.RawConn) error {
			var ctrlErr error
			err := c.Control(func(fd uintptr) {
				ctrlErr = bindSocketToInterface4(windows.Handle(fd), b.realIfaceIndex)
				log.Printf("  relayDirect: bindSocketToInterface4 result: %v", ctrlErr)
			})
			if err != nil {
				return err
			}
			return ctrlErr
		},
	}
	remote, err := dialer.Dial("tcp4", dstAddr)
	if err != nil {
		log.Printf("  relayDirect: dial %s FAILED: %v", dstAddr, err)
		return
	}
	log.Printf("  relayDirect: dial %s OK, local=%s remote=%s, relaying", dstAddr, remote.LocalAddr(), remote.RemoteAddr())
	defer remote.Close()

	var src io.Reader = remote
	if dstPort == 53 {
		src = io.TeeReader(remote, &dnsTCPSink{onMessage: b.observeDNSResponse})
	}

	done := make(chan struct{}, 2)
	go func() {
		n, e := io.Copy(remote, local)
		log.Printf("  relayDirect: local->remote done n=%d err=%v", n, e)
		done <- struct{}{}
	}()
	go func() {
		n, e := io.Copy(local, src)
		log.Printf("  relayDirect: remote->local done n=%d err=%v", n, e)
		done <- struct{}{}
	}()
	<-done
	log.Printf("  relayDirect: %s finished", dstAddr)
}

func portStr(p uint16) string {
	return strconv.Itoa(int(p))
}

func (b *Bridge) pumpWintunToStack() {
	defer b.pumpsDone.Done()
	for {
		if b.closeFlag.Load() {
			return
		}
		packet, err := b.session.ReceivePacket()
		switch err {
		case nil:
			if len(packet) > 0 {
				b.pumpPacketCount++
				if b.pumpPacketCount <= 200 && len(packet) >= 20 && packet[0]>>4 == 4 {
					proto := packet[9]
					src := net.IP(packet[12:16])
					dst := net.IP(packet[16:20])
					log.Printf("  [pkt #%d] IPv4 proto=%d %s -> %s (%d bytes)", b.pumpPacketCount, proto, src, dst, len(packet))
				}
				pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
					Payload: buffer.MakeWithData(append([]byte(nil), packet...)),
				})
				switch packet[0] >> 4 {
				case 4:
					b.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
				case 6:
					b.ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
				}
				pkb.DecRef()
			}
			b.session.ReleaseReceivePacket(packet)
		case windows.ERROR_NO_MORE_ITEMS:
			windows.WaitForSingleObject(b.readWait, windows.INFINITE)
		case windows.ERROR_HANDLE_EOF:
			log.Printf("wintun session closed")
			return
		default:
			log.Printf("wintun ReceivePacket error: %v", err)
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (b *Bridge) pumpStackToWintun(ctx context.Context) {
	defer b.pumpsDone.Done()
	for {
		if b.closeFlag.Load() {
			return
		}
		pkt := b.ep.ReadContext(ctx)
		if pkt == nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		view := pkt.ToView()
		pkt.DecRef()
		n := view.Size()
		out, err := b.session.AllocateSendPacket(n)
		if err != nil {
			log.Printf("wintun AllocateSendPacket error: %v", err)
			continue
		}
		view.Read(out)
		b.session.SendPacket(out)
	}
}

func bindSocketToInterface4(handle windows.Handle, interfaceIndex uint32) error {
	const ipUnicastIf = 31
	// MSDN: for IPv4 this needs to be in net byte order (like an IP
	// address with leading zeros) — same gotcha amneziawg-go's own
	// conn/bind_windows.go works around for the tunnel's own UDP socket.
	be := (interfaceIndex>>24)&0xff | (interfaceIndex>>8)&0xff00 | (interfaceIndex<<8)&0xff0000 | (interfaceIndex<<24)&0xff000000
	return windows.SetsockoptInt(handle, windows.IPPROTO_IP, ipUnicastIf, int(be))
}
