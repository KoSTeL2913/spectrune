package main

import (
	"strconv"
	"strings"
)

// uapiStats reads a WireGuard device's IpcGet() dump: whether any peer has
// completed a handshake, and the bytes received/sent summed over all
// peers. Shared by the Windows tunnels and the Linux bridge — the GUI
// shows the totals as live speed and the handshake as Connected.
func uapiStats(uapi string) (handshake bool, rx, tx uint64) {
	for _, line := range strings.Split(uapi, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "last_handshake_time_sec":
			if val != "" && val != "0" {
				handshake = true
			}
		case "rx_bytes":
			n, _ := strconv.ParseUint(val, 10, 64)
			rx += n
		case "tx_bytes":
			n, _ := strconv.ParseUint(val, 10, 64)
			tx += n
		}
	}
	return handshake, rx, tx
}
