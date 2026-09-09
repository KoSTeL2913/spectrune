// The Linux per-app-routing engine, v2 — replaces the network-namespace
// approach (see git history / the plan file for that version) after the
// user explicitly asked for the ability to route an *already-running*
// process's traffic without relaunching it, matching Windows' live-
// intercept behavior. Network namespaces can't do that: namespace
// membership is fixed at process creation, and moving an already-running
// multi-process/multi-threaded app (a browser's many content processes)
// into a different one after the fact isn't practically supported.
//
// Instead: a cgroup v2 ("/sys/fs/cgroup/spectrune") + fwmark + policy
// routing, the standard Linux split-tunnel technique. The TUN device
// lives in the root namespace now (no netns at all) with its own routing
// table that only packets carrying the mark ever use:
//   - iptables -t mangle -A OUTPUT -m cgroup --path spectrune -j MARK --set-mark <fwMark>
//   - ip rule add fwmark <fwMark> table <routeTable>
//   - ip route add default dev spectrune0 table <routeTable>
//
// Moving a PID into the cgroup (echo pid > cgroup.procs) immediately
// starts marking *that process's future packets* — including a process
// that was already running and already had other connections open
// (those existing connections keep their original routing; only new ones
// made after the move pick up the tunnel, same caveat every similar tool
// has). Cgroup membership is inherited by fork(), so once a process is
// in the cgroup, everything it forks afterward (a browser's new tab
// processes) is too, automatically — no need to track descendants by hand.
//
// A background matcher (matchLoop) continuously scans /proc for
// processes matching the connected profile's IncludedApps and moves any
// not yet in the cgroup — this is what makes "just open the app
// normally, no special launch step" work, mirroring the Windows
// experience instead of requiring an explicit "Launch" click.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

const (
	tunName      = "spectrune0"
	cgroupRoot   = "/sys/fs/cgroup/spectrune"
	cgroupRelDir = "spectrune" // relative to the cgroup2 mount, for iptables --path
	fwMark       = "0xca11"    // arbitrary; confirmed live 2026-09 that the real AmneziaWG kernel client already uses 0xca6c on this machine, so pick something else
	routeTable   = "100"

	matchInterval = time.Second
)

// LinuxBridge owns one running tunnel + its cgroup/routing setup — same
// public shape (Start/Stop/HandshakeOK) as the netns-based version it
// replaces, so service_linux.go didn't need to change.
type LinuxBridge struct {
	dev     *device.Device
	sniffer *domainSniffer

	mu           sync.Mutex
	includedApps []string // binary basenames/paths resolved from the profile's IncludedApps desktop IDs
	stopMatch    chan struct{}
}

func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runCmdIgnoreErr(name string, args ...string) {
	if err := runCmd(name, args...); err != nil {
		log.Printf("(non-fatal) %v", err)
	}
}

// teardownRouting removes every piece Start sets up, best-effort and
// idempotent — called both by Stop() and defensively at the start of
// Start() in case a previous run didn't shut down cleanly.
func teardownRouting() {
	runCmdIgnoreErr("iptables", "-t", "mangle", "-D", "OUTPUT", "-m", "cgroup", "--path", cgroupRelDir, "-j", "MARK", "--set-mark", fwMark)
	runCmdIgnoreErr("iptables", "-t", "mangle", "-D", "OUTPUT", "-m", "set", "--match-set", domainSetName, "dst", "-j", "MARK", "--set-mark", fwMark)
	runCmdIgnoreErr("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", tunName, "-j", "MASQUERADE")
	runCmdIgnoreErr("ip", "rule", "del", "fwmark", fwMark, "table", routeTable)
	runCmdIgnoreErr("ip", "route", "flush", "table", routeTable)
	runCmdIgnoreErr("ip", "link", "del", tunName)
	runCmdIgnoreErr("ipset", "destroy", domainSetName)
	os.RemoveAll(cgroupRoot)
}

// Start brings the tunnel up: TUN + AmneziaWG device in the root
// namespace, plus cgroup + fwmark + policy routing so only cgroup-tagged
// processes' packets ever use it.
//
// DNS note: a cgroup-tagged process's own DNS query to the local
// systemd-resolved stub (127.0.0.53) is *not* tunneled — the actual
// upstream query happens in systemd-resolved's own process, which is
// never in our cgroup, over the regular network. A DNAT rule redirecting
// port-53 traffic straight to a real resolver was tried specifically to
// close this gap, but broke DNS outright (confirmed live 2026-09-09:
// direct queries to 1.1.1.1 worked instantly, 127.0.0.53 redirected to
// the exact same 1.1.1.1 via DNAT timed out every time — some kernel
// quirk around NAT-ing a loopback-destined packet to a non-loopback
// address that wasn't worth chasing further given plain, un-DNAT'd
// resolution already works end-to-end). So: domain names an included
// app looks up may leak over the regular network as metadata, but the
// actual traffic to whatever IP that resolves to is still correctly
// tunneled (verified: curl to a resolved hostname returns the VPN
// server's IP). Same trade-off several similar split-tunnel tools make.
func (b *LinuxBridge) Start(cfg *LinuxConfig) error {
	uapiConf, err := cfg.ToUAPI()
	if err != nil {
		return fmt.Errorf("ToUAPI: %w", err)
	}

	teardownRouting() // best-effort cleanup of a previous unclean shutdown

	if err := os.MkdirAll(cgroupRoot, 0o755); err != nil {
		return fmt.Errorf("MkdirAll %s: %w", cgroupRoot, err)
	}
	if err := runCmd("ipset", "create", domainSetName, "hash:ip", "-exist"); err != nil {
		return fmt.Errorf("ipset create: %w", err)
	}

	tdev, err := tun.CreateTUN(tunName, device.DefaultMTU)
	if err != nil {
		return fmt.Errorf("tun.CreateTUN: %w", err)
	}
	logger := device.NewLogger(device.LogLevelError, "[spectrune] ")
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), logger)
	if err := dev.IpcSet(uapiConf); err != nil {
		dev.Close()
		return fmt.Errorf("IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("Up: %w", err)
	}
	b.dev = dev

	steps := [][]string{
		{"ip", "addr", "add", cfg.Address, "dev", tunName},
		{"ip", "link", "set", tunName, "up"},
		{"ip", "route", "add", "default", "dev", tunName, "table", routeTable},
		{"ip", "rule", "add", "fwmark", fwMark, "table", routeTable},
		{"iptables", "-t", "mangle", "-A", "OUTPUT", "-m", "cgroup", "--path", cgroupRelDir, "-j", "MARK", "--set-mark", fwMark},
		// Without this, packets routed out spectrune0 keep whatever
		// source address the sending socket picked at connect() time —
		// which happens *before* the mangle-OUTPUT mark is applied, so
		// it's always the real physical interface's address (e.g. the
		// WiFi IP), never the tunnel's own 10.4.0.x. AmneziaWG/WireGuard
		// servers check that a peer's decrypted traffic's source address
		// matches that peer's assigned address and silently drop it
		// otherwise (a standard anti-spoofing measure) — confirmed live
		// 2026-09-09 via tcpdump on spectrune0 showing the real LAN IP as
		// source, exactly matching the symptom (tx bytes climbing, rx
		// permanently zero, handshake itself still fine since it's a
		// control message with no encapsulated "inner" IP packet to
		// check). MASQUERADE rewrites the source to the tunnel's own
		// address as the packet leaves via spectrune0.
		{"iptables", "-t", "nat", "-A", "POSTROUTING", "-o", tunName, "-j", "MASQUERADE"},
		// Domain-based routing (dnssniff_linux.go): any packet whose
		// destination IP was resolved from a watched domain gets the same
		// mark, regardless of which process sent it — there's no cgroup
		// for "traffic to a domain" the way there is for an app.
		{"iptables", "-t", "mangle", "-A", "OUTPUT", "-m", "set", "--match-set", domainSetName, "dst", "-j", "MARK", "--set-mark", fwMark},
	}
	for _, s := range steps {
		if err := runCmd(s[0], s[1:]...); err != nil {
			b.Stop()
			return err
		}
	}

	b.mu.Lock()
	b.includedApps = resolveAppBinaries(cfg.IncludedApps)
	b.stopMatch = make(chan struct{})
	stopCh := b.stopMatch
	b.mu.Unlock()
	go b.matchLoop(stopCh)

	sniffer := newDomainSniffer()
	sniffer.setDomains(resolveDomainLists(cfg.IncludedDomainLists))
	if err := sniffer.start(); err != nil {
		log.Printf("domainSniffer.start: %v (domain-based routing unavailable this session)", err)
	} else {
		b.sniffer = sniffer
	}

	log.Printf("Spectrune routing up (cgroup %s, fwmark %s), tunnel to %s", cgroupRoot, fwMark, cfg.Endpoint)
	return nil
}

// Stop tears everything down — safe on a partially-started bridge and
// safe to call twice.
func (b *LinuxBridge) Stop() {
	b.mu.Lock()
	if b.stopMatch != nil {
		close(b.stopMatch)
		b.stopMatch = nil
	}
	b.mu.Unlock()

	if b.sniffer != nil {
		b.sniffer.close()
		b.sniffer = nil
	}
	if b.dev != nil {
		b.dev.Close()
		b.dev = nil
	}
	teardownRouting()
}

// UpdateIncludedApps swaps the live app selection without a reconnect —
// same reasoning as bridge_windows.go's UpdateIncludedApps: editing the
// Apps list while connected shouldn't require tearing down the tunnel.
func (b *LinuxBridge) UpdateIncludedApps(apps []string) {
	b.mu.Lock()
	b.includedApps = resolveAppBinaries(apps)
	b.mu.Unlock()
}

// UpdateIncludedDomainLists swaps which domain lists are enabled without
// a reconnect — same reasoning as UpdateIncludedApps.
func (b *LinuxBridge) UpdateIncludedDomainLists(listNames []string) {
	if b.sniffer != nil {
		b.sniffer.setDomains(resolveDomainLists(listNames))
	}
}

// matchLoop periodically scans /proc for already-running processes that
// match the current app selection and haven't been moved into the
// cgroup yet. This is what makes opening an included app *normally*
// (double-clicking its usual icon, or it was already running before
// Connect) work without any special launch step.
func (b *LinuxBridge) matchLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(matchInterval)
	defer ticker.Stop()
	already := make(map[int]bool)
	for {
		b.mu.Lock()
		targets := append([]string(nil), b.includedApps...)
		b.mu.Unlock()
		if len(targets) > 0 {
			for _, pid := range matchingPIDs(targets) {
				if already[pid] {
					continue
				}
				if err := addPIDToCgroup(pid); err != nil {
					// Common and harmless: the process exited between
					// being listed and us trying to move it.
					continue
				}
				already[pid] = true
				log.Printf("Spectrune: included pid %d into the tunnel", pid)
			}
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func addPIDToCgroup(pid int) error {
	return os.WriteFile(filepath.Join(cgroupRoot, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644)
}

// HandshakeOK reports whether the WireGuard/AmneziaWG handshake with the
// peer has actually completed — unchanged from the netns-based version.
func (b *LinuxBridge) HandshakeOK() bool {
	if b.dev == nil {
		return false
	}
	state, err := b.dev.IpcGet()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(state, "\n") {
		if strings.HasPrefix(line, "last_handshake_time_sec=") {
			return strings.TrimPrefix(line, "last_handshake_time_sec=") != "0"
		}
	}
	return false
}
