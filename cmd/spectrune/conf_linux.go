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
	"net"
	"net/netip"
	"strings"
	"time"
)

// LinuxConfig mirrors just the fields Spectrune Linux actually uses — one
// interface, one peer (matching every profile this app has ever supported
// on Windows too; multi-peer was never a requirement).
type LinuxConfig struct {
	Name       string
	PrivateKey string // base64, as read from the file
	Address    string // "10.x.x.x/24" — first Address= line only
	DNS        []string
	// AmneziaExtra holds the AmneziaWG obfuscation keys (see amneziaParams)
	// as "key=value" with the key lower-cased as in the file and the value
	// raw — ToUAPI and ToWgQuick translate per key, mirroring the Windows
	// conf package's ToUAPI/ToWgQuick.
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
	Hotkey       string // "Ctrl+Alt+F1" form, see hotkeys_linux.go

	// IncludedDomainLists holds the *names* of enabled domain lists
	// (domainlists_linux.go) — not domains directly, so the same named
	// list (e.g. "тг") can be toggled on/off per profile and edited in
	// one place rather than duplicated into every profile that uses it.
	IncludedDomainLists []string
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

// amneziaParam describes one AmneziaWG [Interface] key: how it is spelled
// when written back to the file, its UAPI name (empty = the engine has no
// such key, so it is only kept in the file) and how its value converts.
type amneziaParam struct {
	fileName string
	uapiName string
	conv     func(string) (string, error)
}

// amneziaParams covers AWG 1.0 (Jc..H4), 2.0 (S3/S4, H ranges, I1-I5) and
// 3.x (HeaderProtectionKey and the timing/padding keys) — the same set the
// Windows conf package parses. J1-J3/Itime are AWG 1.5 leftovers the
// amneziawg-go v3 engine no longer accepts; Windows drops them too.
var amneziaParams = map[string]amneziaParam{
	"jc":                     {"Jc", "jc", nil},
	"jmin":                   {"Jmin", "jmin", nil},
	"jmax":                   {"Jmax", "jmax", nil},
	"s1":                     {"S1", "s1", nil},
	"s2":                     {"S2", "s2", nil},
	"s3":                     {"S3", "s3", nil},
	"s4":                     {"S4", "s4", nil},
	"h1":                     {"H1", "h1", nil},
	"h2":                     {"H2", "h2", nil},
	"h3":                     {"H3", "h3", nil},
	"h4":                     {"H4", "h4", nil},
	"i1":                     {"I1", "i1", nil},
	"i2":                     {"I2", "i2", nil},
	"i3":                     {"I3", "i3", nil},
	"i4":                     {"I4", "i4", nil},
	"i5":                     {"I5", "i5", nil},
	"j1":                     {"J1", "", nil},
	"j2":                     {"J2", "", nil},
	"j3":                     {"J3", "", nil},
	"itime":                  {"Itime", "", nil},
	"headerprotectionkey":    {"HeaderProtectionKey", "header_protection_key", b64ToHex},
	"contentpaddingaddition": {"ContentPaddingAddition", "content_padding_addition", nil},
	"rekeyaftertime":         {"RekeyAfterTime", "rekey_after_time", nil},
	"rekeytimeout":           {"RekeyTimeout", "rekey_timeout", nil},
	"rejectaftertime":        {"RejectAfterTime", "reject_after_time", nil},
	"keepalivetimeout":       {"KeepaliveTimeout", "keepalive_timeout", nil},
	"maxhandshakeattempts":   {"MaxHandshakeAttempts", "max_handshake_attempts", nil},
	"randomtrailers":         {"RandomTrailers", "random_trailers", boolToUAPI},
	"disablecookies":         {"DisableCookies", "disable_cookies", boolToUAPI},
}

func isAmneziaParam(key string) bool {
	_, ok := amneziaParams[key]
	return ok
}

// boolToUAPI turns awg-quick's on/off/true/... into the engine's 1/0.
func boolToUAPI(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "t", "yes":
		return "1", nil
	case "off", "0", "false", "f", "no":
		return "0", nil
	}
	return "", fmt.Errorf("not a boolean: %q", v)
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
			// Empty I1-I5 / AWG 3 keys mean "unset" in configs the
			// Amnezia apps export — skip rather than send key= to the engine.
			if val != "" {
				cfg.AmneziaExtra = append(cfg.AmneziaExtra, key+"="+val)
			}
		case section == "interface" && key == "includedapps":
			for _, a := range strings.Split(val, ",") {
				if a = strings.TrimSpace(a); a != "" {
					cfg.IncludedApps = append(cfg.IncludedApps, a)
				}
			}
		case section == "interface" && key == "domainlists":
			for _, a := range strings.Split(val, ",") {
				if a = strings.TrimSpace(a); a != "" {
					cfg.IncludedDomainLists = append(cfg.IncludedDomainLists, a)
				}
			}
		case section == "interface" && key == "autoconnect":
			cfg.AutoConnect = strings.EqualFold(val, "true")
		case section == "interface" && key == "hotkey":
			cfg.Hotkey = val
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
		key, val, _ := strings.Cut(e, "=")
		p, ok := amneziaParams[key]
		if !ok || p.uapiName == "" {
			continue
		}
		if p.conv != nil {
			if val, err = p.conv(val); err != nil {
				return "", fmt.Errorf("%s: %w", p.fileName, err)
			}
		}
		fmt.Fprintf(&b, "%s=%s\n", p.uapiName, val)
	}
	fmt.Fprintf(&b, "public_key=%s\n", pubHex)
	if c.PeerPresharedKey != "" {
		pskHex, err := b64ToHex(c.PeerPresharedKey)
		if err != nil {
			return "", fmt.Errorf("PresharedKey: %w", err)
		}
		fmt.Fprintf(&b, "preshared_key=%s\n", pskHex)
	}
	endpoint, err := resolveEndpoint(c.Endpoint)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	for _, ip := range c.AllowedIPs {
		fmt.Fprintf(&b, "allowed_ip=%s\n", ip)
	}
	return b.String(), nil
}

// resolveEndpoint turns "host:port" into "ip:port" — the UAPI only takes
// addresses, and a hostname failed with "ParseAddr: unexpected character"
// (reported 2026-10-23, the first config with a domain endpoint). Windows
// gets this from conf.ToUAPI's own resolver. IPv4 is preferred, as the
// tunnel and its bypass routing are IPv4. A few retries cover the network
// still coming up right after boot or wake.
func resolveEndpoint(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("Endpoint %q: %w", endpoint, err)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return endpoint, nil
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			lastErr = err
			continue
		}
		var v6 net.IP
		for _, ip := range ips {
			if ip.To4() != nil {
				return net.JoinHostPort(ip.String(), port), nil
			}
			if v6 == nil {
				v6 = ip
			}
		}
		if v6 != nil {
			return net.JoinHostPort(v6.String(), port), nil
		}
		lastErr = fmt.Errorf("no addresses")
	}
	return "", fmt.Errorf("Endpoint %s: can't resolve %s: %w", endpoint, host, lastErr)
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
		key, val, ok := strings.Cut(e, "=")
		if p, known := amneziaParams[key]; ok && known {
			fmt.Fprintf(&b, "%s = %s\n", p.fileName, val)
		}
	}
	if len(c.IncludedDomainLists) > 0 {
		fmt.Fprintf(&b, "DomainLists = %s\n", strings.Join(c.IncludedDomainLists, ", "))
	}
	if len(c.IncludedApps) > 0 {
		fmt.Fprintf(&b, "IncludedApps = %s\n", strings.Join(c.IncludedApps, ", "))
	}
	if c.AutoConnect {
		fmt.Fprintf(&b, "AutoConnect = true\n")
	}
	if c.Hotkey != "" {
		fmt.Fprintf(&b, "Hotkey = %s\n", c.Hotkey)
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
