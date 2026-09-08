// The Linux per-app-routing engine: a dedicated network namespace holding
// an AmneziaWG-backed TUN interface, verified working 2026-09-08 (see
// ~/.claude/plans/idempotent-nibbling-hinton.md's Phase-1 spike — this is
// that spike promoted into the real app, not a rewrite). Architecturally
// different from the Windows Bridge (bridge_windows.go): there is no
// packet interception or per-PID matching here at all. Instead, an app
// picked in the GUI is *launched* inside this namespace (apps_linux.go's
// LaunchApp), and the kernel's own routing does the rest — anything
// running inside the namespace goes through the tunnel, anything outside
// it doesn't, full stop.
//
// The core trick (wireguard.com/netns): the TUN device and its
// device.Device are created while this process is still in the root
// namespace, so the encrypted UDP transport socket binds there and keeps
// working over the real physical interface. Only the resulting netdevice
// is then moved into the isolated namespace via "ip link set netns" —
// nothing else needs to cross namespaces, and no veth pair is needed.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

const (
	netnsName = "spectrune0"
	tunName   = "spectrune0"
)

// LinuxBridge owns one running tunnel + namespace — the Linux analog of
// bridge_windows.go's Bridge, with Start/Stop following the exact same
// shape so service_linux.go's Service can mirror service_windows.go's RPC
// contract almost verbatim.
type LinuxBridge struct {
	dev *device.Device
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

// Start brings the tunnel up: creates the TUN + AmneziaWG device in the
// root namespace, then moves the interface into a dedicated namespace and
// configures it as that namespace's default route. Any failure tears down
// whatever was already created before returning.
func (b *LinuxBridge) Start(cfg *LinuxConfig) error {
	uapiConf, err := cfg.ToUAPI()
	if err != nil {
		return fmt.Errorf("ToUAPI: %w", err)
	}

	// Best-effort pre-cleanup — an unclean previous shutdown (crash, kill
	// -9) can leave the namespace behind, which "ip netns add" would
	// otherwise fail on.
	runCmdIgnoreErr("ip", "netns", "del", netnsName)

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

	if err := runCmd("ip", "netns", "add", netnsName); err != nil {
		b.Stop()
		return err
	}
	if err := runCmd("ip", "link", "set", tunName, "netns", netnsName); err != nil {
		b.Stop()
		return err
	}

	steps := [][]string{
		{"ip", "-n", netnsName, "link", "set", "lo", "up"},
		{"ip", "-n", netnsName, "addr", "add", cfg.Address, "dev", tunName},
		{"ip", "-n", netnsName, "link", "set", tunName, "up"},
		{"ip", "-n", netnsName, "route", "add", "default", "dev", tunName},
	}
	for _, s := range steps {
		if err := runCmd(s[0], s[1:]...); err != nil {
			b.Stop()
			return err
		}
	}

	// See netns_linux.go's package doc: systemd-resolved's stub resolver
	// (127.0.0.53, what /etc/resolv.conf usually points at) lives in the
	// ROOT namespace's loopback and is unreachable from inside an isolated
	// one. "ip netns exec" bind-mounts /etc/netns/<ns>/resolv.conf over
	// /etc/resolv.conf if it exists — without this, every hostname lookup
	// by an app launched into the namespace fails outright, independent of
	// whether the tunnel itself works. Use the profile's own DNS if it set
	// one, else a sane public default.
	dns := "1.1.1.1"
	if len(cfg.DNS) > 0 {
		dns = cfg.DNS[0]
	}
	if err := os.MkdirAll("/etc/netns/"+netnsName, 0o755); err != nil {
		b.Stop()
		return fmt.Errorf("MkdirAll /etc/netns/%s: %w", netnsName, err)
	}
	if err := os.WriteFile("/etc/netns/"+netnsName+"/resolv.conf", []byte("nameserver "+dns+"\n"), 0o644); err != nil {
		b.Stop()
		return fmt.Errorf("writing resolv.conf: %w", err)
	}

	log.Printf("Spectrune namespace %q up, tunnel to %s", netnsName, cfg.Endpoint)
	return nil
}

// Stop tears everything down — safe to call on a partially-started bridge
// (Start's own failure paths reuse it) or twice in a row.
func (b *LinuxBridge) Stop() {
	if b.dev != nil {
		b.dev.Close()
		b.dev = nil
	}
	runCmdIgnoreErr("ip", "netns", "del", netnsName)
	os.RemoveAll("/etc/netns/" + netnsName)
}

// HandshakeOK reports whether the WireGuard/AmneziaWG handshake with the
// peer has actually completed — same UAPI text parsing as
// tunnel_windows.go's hasHandshake, same reasoning: "the namespace exists"
// alone doesn't mean traffic can actually get anywhere.
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
