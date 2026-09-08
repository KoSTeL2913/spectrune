// A small, self-contained wg-quick-style config reader/writer for Linux —
// deliberately NOT the shared github.com/amnezia-vpn/amneziawg-windows/v3/conf
// package the Windows build uses: that package's portable parsing logic
// (parser.go/writer.go) is entangled with Windows-only DPAPI storage and an
// l18n package that itself imports golang.org/x/sys/windows, so importing
// any part of it drags in build constraints that exclude all files on
// Linux. Splitting that apart properly is real work on a shared dependency
// with an already-shipping Windows build depending on it — not worth doing
// before this Linux port has proven itself out. This file covers exactly
// what Spectrune Linux needs: wg-quick text <-> LinuxConfig <-> the UAPI
// text device.Device.IpcSet expects.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// LinuxConfig mirrors just the fields Spectrune Linux actually uses — one
// interface, one peer (matching every profile this app has ever supported
// on Windows too; multi-peer was never a requirement).
type LinuxConfig struct {
	Name       string
	PrivateKey string // base64, as read from the file
	Address    string // "10.x.x.x/24" — first Address= line only
	DNS        []string
	// AmneziaExtra holds jc/jmin/jmax/s1/s2/h1-h4 verbatim (lower-cased
	// keys, raw values) — passed straight through into the UAPI config
	// text unmodified, same as tunnel.go's cfg.ToUAPI() does on Windows.
	AmneziaExtra []string

	PeerPublicKey    string // base64
	PeerPresharedKey string // base64, optional
	Endpoint         string
	AllowedIPs       []string

	// IncludedApps holds .desktop entry IDs (e.g. "org.mozilla.firefox")
	// selected via the app picker — GUI-only, mirrors the Windows profile's
	// IncludedApps field, but "included" here means "launchable into this
	// tunnel's namespace" rather than "intercepted and matched by PID".
	IncludedApps []string
	AutoConnect  bool
}

func b64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key must decode to exactly 32 bytes, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func isAmneziaParam(key string) bool {
	switch key {
	case "jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4":
		return true
	}
	return false
}

// ParseWgQuick reads a wg-quick-style [Interface]/[Peer] config (the same
// format AmneziaWG and every WireGuard client use, including what the
// Windows Spectrune's Apps picker exports) into a LinuxConfig. Only the
// first [Peer] block is read — deliberate, matches the single-peer
// assumption above.
func ParseWgQuick(text, name string) (*LinuxConfig, error) {
	cfg := &LinuxConfig{Name: name}
	section := ""
	sawPeer := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			if section == "peer" {
				if sawPeer {
					break // only the first peer, see doc comment
				}
				sawPeer = true
			}
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])
		switch {
		case section == "interface" && key == "address":
			cfg.Address = strings.TrimSpace(strings.SplitN(val, ",", 2)[0])
		case section == "interface" && key == "privatekey":
			cfg.PrivateKey = val
		case section == "interface" && key == "dns":
			for _, d := range strings.Split(val, ",") {
				if d = strings.TrimSpace(d); d != "" {
					cfg.DNS = append(cfg.DNS, d)
				}
			}
		case section == "interface" && isAmneziaParam(key):
			cfg.AmneziaExtra = append(cfg.AmneziaExtra, key+"="+val)
		case section == "interface" && key == "includedapps":
			for _, a := range strings.Split(val, ",") {
				if a = strings.TrimSpace(a); a != "" {
					cfg.IncludedApps = append(cfg.IncludedApps, a)
				}
			}
		case section == "interface" && key == "autoconnect":
			cfg.AutoConnect = strings.EqualFold(val, "true")
		case section == "peer" && key == "publickey":
			cfg.PeerPublicKey = val
		case section == "peer" && key == "presharedkey":
			cfg.PeerPresharedKey = val
		case section == "peer" && key == "endpoint":
			cfg.Endpoint = val
		case section == "peer" && key == "allowedips":
			for _, a := range strings.Split(val, ",") {
				if a = strings.TrimSpace(a); a != "" {
					cfg.AllowedIPs = append(cfg.AllowedIPs, a)
				}
			}
		}
	}
	if cfg.Address == "" || cfg.PrivateKey == "" || cfg.PeerPublicKey == "" || cfg.Endpoint == "" {
		return nil, fmt.Errorf("config is missing a required field (Address/PrivateKey/PublicKey/Endpoint)")
	}
	if len(cfg.AllowedIPs) == 0 {
		cfg.AllowedIPs = []string{"0.0.0.0/0"}
	}
	return cfg, nil
}

// ToUAPI renders the UAPI config text device.Device.IpcSet expects —
// hex keys, snake_case fields, one allowed_ip line per CIDR.
func (c *LinuxConfig) ToUAPI() (string, error) {
	privHex, err := b64ToHex(c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("PrivateKey: %w", err)
	}
	pubHex, err := b64ToHex(c.PeerPublicKey)
	if err != nil {
		return "", fmt.Errorf("PublicKey: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	fmt.Fprintf(&b, "listen_port=0\n")
	for _, e := range c.AmneziaExtra {
		fmt.Fprintf(&b, "%s\n", e)
	}
	fmt.Fprintf(&b, "public_key=%s\n", pubHex)
	if c.PeerPresharedKey != "" {
		pskHex, err := b64ToHex(c.PeerPresharedKey)
		if err != nil {
			return "", fmt.Errorf("PresharedKey: %w", err)
		}
		fmt.Fprintf(&b, "preshared_key=%s\n", pskHex)
	}
	fmt.Fprintf(&b, "endpoint=%s\n", c.Endpoint)
	for _, ip := range c.AllowedIPs {
		fmt.Fprintf(&b, "allowed_ip=%s\n", ip)
	}
	return b.String(), nil
}

// ToWgQuick renders back to the on-disk file format — used by SaveProfile.
func (c *LinuxConfig) ToWgQuick() string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", c.Address)
	if len(c.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(c.DNS, ", "))
	}
	for _, e := range c.AmneziaExtra {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			fmt.Fprintf(&b, "%s = %s\n", strings.ToUpper(parts[0][:1])+parts[0][1:], parts[1])
		}
	}
	if len(c.IncludedApps) > 0 {
		fmt.Fprintf(&b, "IncludedApps = %s\n", strings.Join(c.IncludedApps, ", "))
	}
	if c.AutoConnect {
		fmt.Fprintf(&b, "AutoConnect = true\n")
	}
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", c.PeerPublicKey)
	if c.PeerPresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", c.PeerPresharedKey)
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(c.AllowedIPs, ", "))
	fmt.Fprintf(&b, "Endpoint = %s\n", c.Endpoint)
	return b.String()
}
