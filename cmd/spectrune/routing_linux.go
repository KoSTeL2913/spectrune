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
// A profile with neither apps nor domain lists selected is a full tunnel (the
// GUI labels it "All traffic"): see applyFullTunnel.
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
//
// It also moves processes back out: one whose app was taken off the list
// (and everything it forked) goes back to the cgroup it came from on the
// next scan, and Stop empties the cgroup before removing it. Before
// v2.0.67.0 neither happened, and a cgroup can't be removed while it
// still holds processes, so an app taken off the list (reported live
// 2026-10-26: Firefox, removed at 14:46) kept going through the VPN
// across disconnects and reconnects until it was restarted.
package main

import (
	"encoding/base64"
	"encoding/json"
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
	cgroupMount  = "/sys/fs/cgroup"
	cgroupRoot   = cgroupMount + "/spectrune"
	cgroupRelDir = "spectrune"        // relative to the cgroup2 mount, for iptables --path
	ownCgroup    = "/" + cgroupRelDir // as /proc/<pid>/cgroup shows it
	// originsFile keeps the origin map across a daemon restart (an
	// update): without it a system service moved in (AnyDesk's) could
	// only be guessed back, and a guess lands it in the root cgroup,
	// outside its systemd unit. /run goes at reboot, as does the cgroup.
	originsFile = "/run/spectrune/cgroup-origins.json"
	fwMark      = "0xca11" // arbitrary; confirmed live 2026-09 that the real AmneziaWG kernel client already uses 0xca6c on this machine, so pick something else
	routeTable  = "100"

	// bypassMark goes on the tunnel's own UDP socket so that in
	// full-tunnel mode the encrypted packets skip table 100 instead of
	// looping back into spectrune0 — the same trick wg-quick uses.
	bypassMark   = 0xca12
	fullRulePrio = "31000" // suppress rule; the catch-all rule takes fullRulePrio+1

	matchInterval = time.Second
)

// LinuxBridge owns one running tunnel + its cgroup/routing setup — same
// public shape (Start/Stop/HandshakeOK) as the netns-based version it
// replaces, so service_linux.go didn't need to change.
type LinuxBridge struct {
	profileName string
	dev         *device.Device
	sniffer     *domainSniffer
	domainDNS   *domainDNSProxy

	mu           sync.Mutex
	includedApps []string // binary basenames/paths resolved from the profile's IncludedApps desktop IDs
	stopMatch    chan struct{}
	matchDone    chan struct{}  // closed when matchLoop has returned
	origin       map[int]string // cgroup each moved process came from, to put it back
	since        time.Time      // when Start brought the tunnel up, for the GUI's connection timer
	dns          []string       // the profile's DNS, applied in full-tunnel mode
	full         bool           // full-tunnel rules currently applied
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
	removeFullTunnel()
	runCmdIgnoreErr("ip", "route", "flush", "table", routeTable)
	runCmdIgnoreErr("ip", "link", "del", tunName)
	runCmdIgnoreErr("ipset", "destroy", domainSetName)
	// Anything still in the cgroup (a previous run that didn't stop
	// cleanly) goes back where it belongs first: a cgroup that still has
	// processes can't be removed, and they'd stay tunneled.
	releaseAll(loadOrigins())
	os.Remove(originsFile)
	if err := os.Remove(cgroupRoot); err != nil && !os.IsNotExist(err) {
		log.Printf("(non-fatal) remove %s: %v", cgroupRoot, err)
	}
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
	b.profileName = cfg.Name
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
	if err := dev.BindSetMark(bypassMark); err != nil {
		dev.Close()
		return fmt.Errorf("BindSetMark: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("Up: %w", err)
	}
	b.dev = dev

	// WireGuard only initiates a handshake once there's an actual packet
	// to send — never just because the device came up. Since nothing is
	// in the cgroup yet at this point (matchLoop hasn't moved any process
	// into it), that packet doesn't exist until the user opens one of the
	// selected apps, so without this, HandshakeOK() (and thus the
	// "Connected" status) can sit on "Connecting…" indefinitely even
	// though the tunnel is otherwise completely healthy — same bug
	// reported on Windows 2026-09-14, same fix: proactively queue an
	// empty keepalive packet, which kicks off the same handshake a real
	// packet would have.
	if pubBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.PeerPublicKey)); err == nil && len(pubBytes) == 32 {
		var pub device.NoisePublicKey
		copy(pub[:], pubBytes)
		if peer := dev.LookupPeer(pub); peer != nil {
			peer.SendKeepalive()
		}
	}

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
	b.dns = cfg.DNS
	b.includedApps = resolveAppBinaries(cfg.IncludedApps)
	b.stopMatch = make(chan struct{})
	b.matchDone = make(chan struct{})
	b.origin = make(map[int]string)
	os.Remove(originsFile)
	stopCh, doneCh := b.stopMatch, b.matchDone
	b.mu.Unlock()
	go b.matchLoop(stopCh, doneCh)

	sniffer := newDomainSniffer()
	sniffer.setDomains(resolveDomainLists(effectiveDomainLists(cfg.Name, cfg.IncludedDomainLists)))
	if err := sniffer.start(); err != nil {
		log.Printf("domainSniffer.start: %v (domain-based routing unavailable this session)", err)
	} else {
		b.sniffer = sniffer
	}

	b.syncFullTunnel(fullTunnelForSelection(len(cfg.IncludedApps), len(cfg.IncludedDomainLists)))
	proxy, err := startDomainDNSProxy(cfg.Address, cfg.DNS, sniffer.handleDNSPayload)
	if err != nil {
		b.Stop()
		return fmt.Errorf("domain DNS proxy: %w", err)
	}
	b.domainDNS = proxy
	if len(effectiveDomainLists(cfg.Name, cfg.IncludedDomainLists)) > 0 {
		if err = proxy.setDomains(resolveDomainLists(effectiveDomainLists(cfg.Name, cfg.IncludedDomainLists))); err != nil {
			b.Stop()
			return err
		}
	}

	b.since = time.Now()
	log.Printf("Spectrune routing up (cgroup %s, fwmark %s), tunnel to %s", cgroupRoot, fwMark, cfg.Endpoint)
	return nil
}

// Stop tears everything down — safe on a partially-started bridge and
// safe to call twice.
func (b *LinuxBridge) Stop() {
	b.mu.Lock()
	done := b.matchDone
	if b.stopMatch != nil {
		close(b.stopMatch)
		b.stopMatch = nil
	}
	b.mu.Unlock()
	if done != nil {
		<-done // so it can't move another process in after the release below
	}
	b.mu.Lock()
	releaseAll(b.origin)
	b.origin = nil
	os.Remove(originsFile)
	b.matchDone = nil
	b.mu.Unlock()

	if b.domainDNS != nil {
		b.domainDNS.close()
		b.domainDNS = nil
	}
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

// UpdateSelection applies both selectors together so switching to domain-only
// routing never briefly enables the full-tunnel catch-all.
func (b *LinuxBridge) UpdateSelection(apps, domainLists []string) {
	b.mu.Lock()
	b.includedApps = resolveAppBinaries(apps)
	b.mu.Unlock()
	b.syncFullTunnel(fullTunnelForSelection(len(apps), len(domainLists)))
	b.UpdateIncludedDomainLists(domainLists)
}

// syncFullTunnel applies or removes the full-tunnel rules to match the
// profile's app and domain-list selection, like Windows'
// syncFullTunnelExtras.
func (b *LinuxBridge) syncFullTunnel(full bool) {
	b.mu.Lock()
	applied, dns := b.full, b.dns
	b.full = full
	b.mu.Unlock()
	switch {
	case full && !applied:
		if err := applyFullTunnel(dns); err != nil {
			log.Printf("full-tunnel mode: %v", err)
		}
	case !full && applied:
		removeFullTunnel()
	}
}

// applyFullTunnel routes everything except the tunnel's own UDP socket
// (bypassMark) through table 100, wg-quick style:
//   - "table main suppress_prefixlength 0" first, so LAN and other
//     specific routes keep working and only the default route is replaced;
//   - "not fwmark bypassMark table 100" for the rest.
//
// The split-mode cgroup/ipset rules stay in place; they're simply
// covered by the catch-all.
//
// IPv6 gets the same rules with an unreachable default — the tunnel is
// IPv4-only (first Address= only), and without it an IPv6-capable network
// would carry v6 traffic straight past the VPN (the leak Windows blocks
// with its IPv6 firewall rule). Unreachable makes browsers fall back to
// IPv4 at once instead of timing out.
//
// DNS: systemd-resolved is pointed at the profile's DNS (1.1.1.1 if it
// has none, as on Windows) through spectrune0 with the "~." routing
// domain, so lookups go through the tunnel instead of the LAN router.
// Best-effort: no resolved, no DNS change, but traffic is still tunneled.
// The settings vanish together with the link on teardown.
func applyFullTunnel(dns []string) error {
	bypassMarkHex := fmt.Sprintf("%#x", bypassMark)
	// Replies to the tunnel's marked socket must pass rp_filter's check
	// against the mark-aware route lookup — same as wg-quick.
	runCmdIgnoreErr("sysctl", "-q", "-w", "net.ipv4.conf.all.src_valid_mark=1")
	catchAllPrio := nextPrio(fullRulePrio)
	steps := [][]string{
		{"ip", "rule", "add", "table", "main", "suppress_prefixlength", "0", "priority", fullRulePrio},
		{"ip", "rule", "add", "not", "fwmark", bypassMarkHex, "table", routeTable, "priority", catchAllPrio},
	}
	for _, s := range steps {
		if err := runCmd(s[0], s[1:]...); err != nil {
			removeFullTunnel()
			return err
		}
	}
	runCmdIgnoreErr("ip", "-6", "route", "add", "unreachable", "default", "table", routeTable)
	runCmdIgnoreErr("ip", "-6", "rule", "add", "table", "main", "suppress_prefixlength", "0", "priority", fullRulePrio)
	runCmdIgnoreErr("ip", "-6", "rule", "add", "not", "fwmark", bypassMarkHex, "table", routeTable, "priority", catchAllPrio)

	if len(dns) == 0 {
		dns = []string{"1.1.1.1"}
	}
	if _, err := exec.LookPath("resolvectl"); err == nil {
		runCmdIgnoreErr("resolvectl", append([]string{"dns", tunName}, dns...)...)
		runCmdIgnoreErr("resolvectl", "domain", tunName, "~.")
		runCmdIgnoreErr("resolvectl", "default-route", tunName, "true")
	}
	log.Printf("full-tunnel mode: all traffic goes through %s (DNS %s)", tunName, strings.Join(dns, ", "))
	return nil
}

// removeFullTunnel undoes applyFullTunnel — best-effort and idempotent
// (called from teardownRouting too). The DNS settings would go with the
// link anyway, but a switch to split mode keeps the link, so revert them.
func removeFullTunnel() {
	catchAllPrio := nextPrio(fullRulePrio)
	for _, fam := range []string{"-4", "-6"} {
		runCmdQuiet("ip", fam, "rule", "del", "priority", fullRulePrio)
		runCmdQuiet("ip", fam, "rule", "del", "priority", catchAllPrio)
	}
	runCmdQuiet("ip", "-6", "route", "flush", "table", routeTable)
	runCmdQuiet("resolvectl", "revert", tunName)
}

func nextPrio(p string) string {
	n, _ := strconv.Atoi(p)
	return strconv.Itoa(n + 1)
}

// runCmdQuiet is runCmdIgnoreErr without the log line, for cleanup that
// usually has nothing to remove (split mode, a fresh Start).
func runCmdQuiet(name string, args ...string) {
	exec.Command(name, args...).Run()
}

// UpdateIncludedDomainLists swaps which domain lists are enabled without
// a reconnect — same reasoning as UpdateIncludedApps.
func (b *LinuxBridge) UpdateIncludedDomainLists(listNames []string) {
	if b.sniffer != nil {
		b.sniffer.setDomains(resolveDomainLists(effectiveDomainLists(b.profileName, listNames)))
		if b.domainDNS != nil && !b.full {
			if err := b.domainDNS.setDomains(resolveDomainLists(effectiveDomainLists(b.profileName, listNames))); err != nil {
				log.Printf("domain DNS: %v", err)
			}
		}
	}
}

// matchLoop periodically scans /proc for already-running processes that
// match the current app selection and haven't been moved into the
// cgroup yet. This is what makes opening an included app *normally*
// (double-clicking its usual icon, or it was already running before
// Connect) work without any special launch step. The same pass moves out
// whatever no longer belongs: see releaseStale.
func (b *LinuxBridge) matchLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(matchInterval)
	defer ticker.Stop()
	for {
		b.mu.Lock()
		targets := append([]string(nil), b.includedApps...)
		b.mu.Unlock()
		want := make(map[int]bool)
		if len(targets) > 0 {
			for _, pid := range matchingPIDs(targets) {
				want[pid] = true
				from := cgroupOf(pid)
				if from == "" || from == ownCgroup {
					continue // gone, or already in
				}
				if err := addPIDToCgroup(pid); err != nil {
					// Common and harmless: the process exited between
					// being listed and us trying to move it.
					continue
				}
				b.mu.Lock()
				b.origin[pid] = from
				saveOrigins(b.origin)
				b.mu.Unlock()
				log.Printf("Spectrune: included pid %d into the tunnel", pid)
			}
		}
		b.releaseStale(want)
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// releaseStale moves out of the cgroup every process that is neither a
// match for the current selection nor started (at any depth) by one: an
// app taken off the list, and children left behind by an included app
// that has exited. Children of included apps stay, as cgroup inheritance
// put them there on purpose (a browser's tab processes).
func (b *LinuxBridge) releaseStale(want map[int]bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	in := make(map[int]bool)
	changed := false
	for _, pid := range cgroupPIDs() {
		if descendsFrom(pid, want) {
			in[pid] = true
		} else {
			releasePID(pid, b.origin)
		}
	}
	for pid := range b.origin {
		if !in[pid] {
			delete(b.origin, pid) // exited or released: the PID may be reused
			changed = true
		}
	}
	if changed {
		saveOrigins(b.origin)
	}
}

func saveOrigins(origin map[int]string) {
	if data, err := json.Marshal(origin); err == nil {
		os.WriteFile(originsFile, data, 0o600)
	}
}

// loadOrigins reads what a previous run of the daemon left (nil if none).
func loadOrigins() map[int]string {
	data, err := os.ReadFile(originsFile)
	if err != nil {
		return nil
	}
	var origin map[int]string
	if json.Unmarshal(data, &origin) != nil {
		return nil
	}
	return origin
}

func addPIDToCgroup(pid int) error {
	return os.WriteFile(filepath.Join(cgroupRoot, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644)
}

// cgroupPIDs lists the processes currently in our cgroup.
func cgroupPIDs() []int {
	data, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.procs"))
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// cgroupOf returns pid's cgroup v2 path as /proc shows it
// ("/user.slice/user-1000.slice/session-c1.scope"), "" if it's gone.
func cgroupOf(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return p
		}
	}
	return ""
}

// parentPID reads pid's parent from /proc/<pid>/stat (0 if it's gone).
// The fields are counted from the last ')', as the command name in
// parentheses before it may itself hold spaces and parentheses.
func parentPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// descendsFrom reports whether pid or one of its ancestors is in set.
func descendsFrom(pid int, set map[int]bool) bool {
	for p, n := pid, 0; p > 1 && n < 64; p, n = parentPID(p), n+1 {
		if set[p] {
			return true
		}
	}
	return false
}

// homeCgroup is where a process leaving our cgroup goes: the cgroup it
// (or the nearest ancestor we moved in) was in before, or, if we never
// recorded one (left over from a previous run), that of its nearest
// ancestor outside our cgroup — for an app started from the desktop
// that is the very cgroup it was started in.
func homeCgroup(pid int, origin map[int]string) string {
	for p, n := pid, 0; p > 1 && n < 64; p, n = parentPID(p), n+1 {
		if o, ok := origin[p]; ok {
			return o
		}
		if p != pid {
			if cg := cgroupOf(p); cg != "" && cg != ownCgroup {
				return cg
			}
		}
	}
	return "/"
}

// releasePID moves pid back to its home cgroup — or, if that one is gone
// or won't take it (a cgroup with sub-cgroups holds no processes of its
// own), the nearest parent that will, up to the root.
func releasePID(pid int, origin map[int]string) {
	data := []byte(strconv.Itoa(pid))
	for dir := homeCgroup(pid, origin); ; dir = filepath.Dir(dir) {
		err := os.WriteFile(filepath.Join(cgroupMount, dir, "cgroup.procs"), data, 0o644)
		if err == nil {
			log.Printf("Spectrune: released pid %d from the tunnel (back to %s)", pid, dir)
			return
		}
		if dir == "/" || dir == "." {
			if cgroupOf(pid) != "" {
				log.Printf("(non-fatal) release pid %d: %v", pid, err)
			}
			return
		}
	}
}

// releaseAll empties our cgroup (origin may be nil).
func releaseAll(origin map[int]string) {
	for _, pid := range cgroupPIDs() {
		releasePID(pid, origin)
	}
}

// HandshakeOK reports whether the WireGuard/AmneziaWG handshake with the
// peer has actually completed — unchanged from the netns-based version.
func (b *LinuxBridge) HandshakeOK() bool {
	ok, _, _ := b.Stats()
	return ok
}

// Stats is HandshakeOK plus the tunnel's received/sent byte totals.
func (b *LinuxBridge) Stats() (handshake bool, rx, tx uint64) {
	if b.dev == nil {
		return false, 0, 0
	}
	state, err := b.dev.IpcGet()
	if err != nil {
		return false, 0, 0
	}
	return uapiStats(state)
}
