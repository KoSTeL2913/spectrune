// Auto-detects whichever tunnel is currently active in the main AmneziaWG
// app and loads its config directly from the app's own encrypted store —
// so Spectrune always follows whatever the user has selected/connected
// in the GUI, with no manual config.conf copying.
package main

import (
	"fmt"
	"log"
	"strings"

	"golang.org/x/sys/windows/svc/mgr"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"
	"github.com/amnezia-vpn/amneziawg-windows/v3/tunnel/winipcfg"
)

const tunnelServicePrefix = "AmneziaWGTunnel$"

// activeTunnelName returns the name of the tunnel the user currently has
// connected in the app. Tries the per-tunnel Windows service first (the
// "proper" signal — confirmed working in earlier testing, e.g.
// SERVICE_NAME: AmneziaWGTunnel$notebook-Kira-Lis while that tunnel was
// connected). But that per-tunnel service isn't always present — confirmed
// missing on this same machine 2026-09-02 with a tunnel named "My-phone-AWG"
// that was nonetheless genuinely connected (handshaking, passing traffic);
// root cause not pinned down (maybe how the tunnel was created/toggled,
// maybe a side effect of the main app being restarted several times that
// session). So this falls back to a second, independent signal: for every
// saved tunnel config, check whether a network adapter exists with that
// exact name and is operationally up — AmneziaWG/WireGuard always names the
// Wintun adapter after the tunnel, so this is true whenever the tunnel is
// actually running, service or no service.
func activeTunnelName() (string, error) {
	if name, err := activeTunnelNameFromService(); err == nil {
		return name, nil
	}
	return activeTunnelNameFromAdapter()
}

func activeTunnelNameFromService() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("connecting to SCM: %w", err)
	}
	defer m.Disconnect()

	names, err := m.ListServices()
	if err != nil {
		return "", fmt.Errorf("listing services: %w", err)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, tunnelServicePrefix) {
			continue
		}
		svc, err := m.OpenService(name)
		if err != nil {
			continue
		}
		status, err := svc.Query()
		svc.Close()
		if err != nil {
			continue
		}
		if status.State == 4 /* svc.Running */ {
			return strings.TrimPrefix(name, tunnelServicePrefix), nil
		}
	}
	return "", fmt.Errorf("no running AmneziaWG tunnel service found")
}

func activeTunnelNameFromAdapter() (string, error) {
	names, err := conf.ListConfigNames()
	if err != nil {
		return "", fmt.Errorf("listing saved tunnel configs: %w", err)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no saved AmneziaWG tunnel configs found")
	}

	rows, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormal)
	if err != nil {
		return "", fmt.Errorf("GetIfTable2Ex: %w", err)
	}
	up := make(map[string]bool, len(rows))
	for i := range rows {
		if rows[i].OperStatus == winipcfg.IfOperStatusUp {
			up[rows[i].Alias()] = true
		}
	}

	for _, name := range names {
		if up[name] {
			return name, nil
		}
	}
	return "", fmt.Errorf("no saved tunnel has a matching up network adapter")
}

// isServiceRunning reports whether the named Windows service is currently
// in the Running state — used by watchMainTunnel to detect the user
// disconnecting the main AmneziaWG app's tunnel.
func isServiceRunning(name string) bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	svc, err := m.OpenService(name)
	if err != nil {
		return false
	}
	defer svc.Close()
	status, err := svc.Query()
	if err != nil {
		return false
	}
	return status.State == 4 /* svc.Running */
}

// loadActiveConfig finds the currently connected tunnel in the main
// AmneziaWG app and decrypts its config directly from
// %ProgramFiles%\AmneziaWG\Data\Configurations — the same DPAPI store the
// app itself uses (conf.LoadFromName does the decrypt+parse, reusing the
// exact same code path as the real app). Requires SYSTEM privileges: the
// store is encrypted by the Manager service, which runs as LocalSystem,
// and DPAPI keys are tied to that specific account.
func loadActiveConfig() (*conf.Config, string, error) {
	name, err := activeTunnelName()
	if err != nil {
		return nil, "", err
	}
	cfg, err := conf.LoadFromName(name)
	if err != nil {
		return nil, "", fmt.Errorf("decrypting active tunnel %q: %w", name, err)
	}
	log.Printf("auto-detected active tunnel: %s", name)
	return cfg, name, nil
}
