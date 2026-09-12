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

	// outTun is our own, separate outbound AmneziaWG tunnel — nil if no
	// config was supplied (pure pass-through mode, useful for testing the
	// bridge itself before wiring up a real VPN peer).
	outTun *outboundTunnel

	adapter        *wintun.Adapter
	session        wintun.Session
	sessionStarted bool
	stack          *stack.Stack
	ep             *channel.Endpoint

	// closeFlag and pumpsDone coordinate a safe shutdown of the two pump
	// goroutines before the Wintun session/adapter get closed — see Stop.
	closeFlag atomic.Bool
	pumpsDone sync.WaitGroup

	readWait      windows.Handle
	stopStackPump context.CancelFunc

	pumpPacketCount int

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
	procName  string
	viaTunnel bool
	closer    io.Closer
}

// Start brings the bridge up: builds the (optional) outbound AmneziaWG
// tunnel from cfg, creates the Wintun adapter and gVisor netstack, wires
// the TCP/UDP forwarders, and makes the Wintun adapter the system default
// route. cfg may be nil for pure pass-through mode (everything relays
// direct, useful for testing the capture path alone). On any failure,
// whatever was already brought up is torn down before returning the error
// — unlike the old log.Fatalf call sites this replaces, a failed Start
// here does not exit the process, so it must not leak the adapter/session.
func (b *Bridge) Start(cfg *conf.Config) error {
	realIfaceIndex, err := bestOutboundInterface(netip.MustParseAddr("1.1.1.1"))
	if err != nil {
		return fmt.Errorf("bestOutboundInterface: %w (are we even online yet?)", err)
	}
	b.realIfaceIndex = realIfaceIndex
	log.Printf("real outbound interface index: %d", b.realIfaceIndex)

	if cfg != nil {
		outTun, err := newOutboundTunnel(cfg, realIfaceIndex)
		if err != nil {
			return fmt.Errorf("newOutboundTunnel: %w", err)
		}
		b.outTun = outTun
		if len(outTun.includedApps) == 0 {
			log.Printf("outbound AmneziaWG tunnel up, full-tunnel mode (no app selection, everything routed)")
		} else {
			log.Printf("outbound AmneziaWG tunnel up, %d included app(s)", len(outTun.includedApps))
			for p := range outTun.includedApps {
				log.Printf("  includedApp: %q", p)
			}
		}
	} else {
		log.Printf("running in pure pass-through mode, everything relays direct")
	}

	adapter, err := wintun.CreateAdapter(adapterName, tunnelType, &adapterGUID)
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

	if err := configureWindowsInterface(); err != nil {
		// The pumps are already running at this point — go through the
		// same teardown Stop() uses rather than a partial failStart, so
		// they're stopped cleanly before the session/adapter close.
		b.Stop()
		return fmt.Errorf("configureWindowsInterface: %w", err)
	}

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
	if b.outTun == nil || len(b.outTun.includedApps) == 0 {
		if err := blockIPv6Firewall(); err != nil {
			log.Printf("warning: could not block outbound IPv6 (possible IPv6 leak): %v", err)
		}
	}

	if b.outTun != nil && len(b.outTun.includedApps) > 0 {
		log.Printf("Bridge active. Selected apps -> VPN tunnel, everything else -> direct.")
	} else if b.outTun != nil {
		log.Printf("Bridge active. Full tunnel: everything -> VPN tunnel.")
	} else {
		log.Printf("Bridge active in pass-through mode (no config.conf).")
	}
	return nil
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

// UpdateIncludedApps swaps the live app selection on an already-running
// tunnel — see Service.SaveProfile (service.go), which calls this when the
// profile being saved is the one currently connected. No-op in
// pass-through mode (no config.conf, b.outTun == nil). Force-closes any
// already-open connection whose path no longer matches the new selection
// (see the conns field doc) so apps pick up the change without needing a
// full restart.
func (b *Bridge) UpdateIncludedApps(apps []string) {
	if b.outTun == nil {
		return
	}
	b.outTun.updateIncludedApps(apps)
	b.closeStaleConns()
}

// UpdateDomains swaps the live domain-list selection on an already-
// running tunnel — same trigger (Service.SaveProfile) and reasoning as
// UpdateIncludedApps, just for domain-list routing instead of app-based.
// Takes the already-resolved flat domain slice, not list names — the
// caller (service_windows.go) does that resolution the same way
// Connect/newOutboundTunnel do.
func (b *Bridge) UpdateDomains(domains []string) {
	if b.outTun == nil {
		return
	}
	b.outTun.updateDomains(domains)
}

// HandshakeOK reports whether the outbound tunnel has ever completed a
// WireGuard handshake with its peer — see outboundTunnel.hasHandshake's
// doc. True in pass-through mode (b.outTun == nil, no config.conf), since
// there's no peer to hand-shake with in the first place and that mode was
// never claiming to be a real VPN connection.
func (b *Bridge) HandshakeOK() bool {
	if b.outTun == nil {
		return true
	}
	return b.outTun.hasHandshake()
}

// trackConn registers a just-opened relayed connection and returns an id
// to hand back to untrackConn once it finishes.
func (b *Bridge) trackConn(procName string, viaTunnel bool, c io.Closer) uint64 {
	b.connsMu.Lock()
	defer b.connsMu.Unlock()
	if b.conns == nil {
		b.conns = make(map[uint64]*trackedConn)
	}
	b.nextConnID++
	id := b.nextConnID
	b.conns[id] = &trackedConn{procName: procName, viaTunnel: viaTunnel, closer: c}
	return id
}

func (b *Bridge) untrackConn(id uint64) {
	b.connsMu.Lock()
	delete(b.conns, id)
	b.connsMu.Unlock()
}

// closeStaleConns force-closes every tracked connection whose recorded
// path (direct vs tunnel) disagrees with what outTun.matches would decide
// right now — called right after an app-selection update. Closing here
// only closes the app-facing side; the relay goroutine's own deferred
// cleanup tears down the remote leg and untracks it as it unwinds.
func (b *Bridge) closeStaleConns() {
	b.connsMu.Lock()
	var stale []io.Closer
	for _, c := range b.conns {
		if b.outTun.matches(c.procName) != c.viaTunnel {
			stale = append(stale, c.closer)
		}
	}
	b.connsMu.Unlock()
	for _, c := range stale {
		c.Close()
	}
	if len(stale) > 0 {
		log.Printf("closed %d stale connection(s) after app-selection change", len(stale))
	}
}

// Stop tears the bridge down: removes the default route, stops both pump
// goroutines, and closes the Wintun session/adapter. Safe to call once per
// successful Start.
func (b *Bridge) Stop() {
	log.Printf("stopping bridge, removing default route...")
	exec.Command("netsh", "interface", "ipv4", "delete", "route", "0.0.0.0/0", adapterName).Run()
	if err := unblockIPv6Firewall(); err != nil {
		log.Printf("warning: could not remove outbound IPv6 block: %v", err)
	}

	// Stop both pump goroutines and WAIT for them to actually return
	// before touching the session/adapter — closing either while a pump
	// is mid-syscall against it segfaults (this crashed the first version
	// of this shutdown path: ACCESS_VIOLATION inside wintun's
	// ReceivePacket, racing with adapter.Close() on another goroutine).
	// Same pattern amneziawg-go's own tun_windows.go Close() uses.
	b.closeFlag.Store(true)
	windows.SetEvent(b.readWait)
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
}

// configureWindowsInterface assigns bridgeAddr to the real Windows adapter
// and makes it the system default route. Uses netsh (simple, reliable,
// same tool used to debug this by hand) rather than raw IP Helper route
// APIs — fine for this prototype stage.
// ipv6FirewallRuleName is shared with service.go's startup safety net,
// which removes any leftover rule from an unclean shutdown.
const ipv6FirewallRuleName = "SpectruneBlockIPv6"

// blockIPv6Firewall adds a Windows Firewall rule blocking all outbound
// IPv6 traffic (RemoteAddress ::/0), regardless of which interface it
// would otherwise have gone out — see the IPv6 leak guard comment at its
// call site in Start(). Removes any stale rule of the same name first so
// this is idempotent (harmless if called twice, e.g. after a crash left
// one behind despite the startup safety net).
func blockIPv6Firewall() error {
	// -RemoteAddress '::/0' (the natural "all IPv6" CIDR) is rejected
	// outright by New-NetFirewallRule on at least this Windows version —
	// "One or more address prefixes are invalid" — meaning this whole
	// rule silently never applied, every single time, since this was
	// written; the resulting IPv6 leak guard has never actually blocked
	// anything, all failures swallowed by Start()'s best-effort log-and-
	// continue. Confirmed live 2026-09-13. An explicit full-range
	// address (lowest to highest IPv6 address) is accepted instead and
	// covers the exact same address space.
	script := fmt.Sprintf(
		`Remove-NetFirewallRule -DisplayName '%[1]s' -ErrorAction SilentlyContinue; `+
			`New-NetFirewallRule -DisplayName '%[1]s' -Direction Outbound -Action Block -RemoteAddress '::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff' -Profile Any`,
		ipv6FirewallRuleName,
	)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("New-NetFirewallRule: %w (%s)", err, string(out))
	}
	return nil
}

// unblockIPv6Firewall removes the rule blockIPv6Firewall added.
func unblockIPv6Firewall() error {
	script := fmt.Sprintf("Remove-NetFirewallRule -DisplayName '%s' -ErrorAction Stop", ipv6FirewallRuleName)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Remove-NetFirewallRule: %w (%s)", err, string(out))
	}
	return nil
}

func configureWindowsInterface() error {
	cmds := [][]string{
		{"netsh", "interface", "ipv4", "set", "address", "name=" + adapterName, "static", bridgeAddr, "255.255.255.0"},
		{"netsh", "interface", "ipv4", "add", "route", "0.0.0.0/0", adapterName, "metric=1"},
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

	if int(pid) != os.Getpid() && (b.outTun.matches(procName) || b.outTun.matchesDomainIP(id.LocalAddress.String())) {
		log.Printf("  -> TUNNEL (matched %s)", procName)
		go b.relayViaTunnel(local, id.LocalAddress.String(), id.LocalPort, procName)
	} else {
		log.Printf("  -> direct")
		go b.relayDirect(local, dstAddr, id.LocalPort, procName)
	}
}

const udpIdleTimeout = 30 * time.Second

func (b *Bridge) handleUDPForwarded(r *udp.ForwarderRequest) {
	id := r.ID()
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
		onResponse = b.outTun.observeDNSResponse
	}

	if int(pid) != os.Getpid() && (b.outTun.matches(procName) || b.outTun.matchesDomainIP(id.LocalAddress.String())) {
		log.Printf("UDP %s -> %s (pid=%d exe=%q) -> TUNNEL", appAddr, dstAddr, pid, procName)
		go b.relayUDP(local, procName, true, func() (net.Conn, error) {
			addr, err := netip.ParseAddr(id.LocalAddress.String())
			if err != nil {
				return nil, err
			}
			return b.outTun.dialUDP(netip.AddrPortFrom(addr, id.LocalPort))
		}, onResponse)
	} else {
		go b.relayUDP(local, procName, false, func() (net.Conn, error) {
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
// regardless of viaTunnel: an app that isn't itself included can still
// resolve a domain that's in an enabled list, and that resolution needs
// to be observed no matter which path carried the query.
func (b *Bridge) relayUDP(local *gonet.UDPConn, procName string, viaTunnel bool, dial func() (net.Conn, error), onResponse func([]byte)) {
	connID := b.trackConn(procName, viaTunnel, local)
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

func (b *Bridge) relayViaTunnel(local *gonet.TCPConn, dstIP string, dstPort uint16, procName string) {
	connID := b.trackConn(procName, true, local)
	defer b.untrackConn(connID)
	defer local.Close()
	addr, err := netip.ParseAddr(dstIP)
	if err != nil {
		log.Printf("  relayViaTunnel: bad addr %s: %v", dstIP, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remote, err := b.outTun.dial(ctx, netip.AddrPortFrom(addr, dstPort))
	if err != nil {
		log.Printf("  relayViaTunnel: dial %s:%d via tunnel FAILED: %v", dstIP, dstPort, err)
		return
	}
	log.Printf("  relayViaTunnel: dial %s:%d via tunnel OK, relaying", dstIP, dstPort)
	defer remote.Close()

	var src io.Reader = remote
	if dstPort == 53 {
		src = io.TeeReader(remote, &dnsTCPSink{onMessage: b.outTun.observeDNSResponse})
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
	connID := b.trackConn(procName, false, local)
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
		src = io.TeeReader(remote, &dnsTCPSink{onMessage: b.outTun.observeDNSResponse})
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
