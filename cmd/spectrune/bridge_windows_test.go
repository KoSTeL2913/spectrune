package main

import "testing"

func TestShellRouting(t *testing.T) {
	const cmd = `C:\Windows\System32\cmd.exe`
	const powershell = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	const curl = `C:\Windows\System32\curl.exe`
	full := &namedTunnel{name: "full", t: &outboundTunnel{}}
	shell := &namedTunnel{name: "shell", t: &outboundTunnel{includedApps: buildIncludedApps([]string{cmd})}}
	nested := &namedTunnel{name: "nested", t: &outboundTunnel{includedApps: buildIncludedApps([]string{powershell})}}
	explicit := &namedTunnel{name: "curl", t: &outboundTunnel{includedApps: buildIncludedApps([]string{curl})}}
	domain := &namedTunnel{name: "domain", t: &outboundTunnel{includedApps: buildIncludedApps([]string{`C:\other.exe`}), domainIPs: map[string]bool{"203.0.113.1": true}}}
	tests := []struct {
		name      string
		tunnels   []*namedTunnel
		ancestors []string
		want      *namedTunnel
	}{
		{"cmd child", []*namedTunnel{shell}, []string{cmd}, shell},
		{"nested shell", []*namedTunnel{shell, nested}, []string{powershell, cmd}, nested},
		{"explicit child overrides parent", []*namedTunnel{shell, explicit}, []string{cmd}, explicit},
		{"parent overrides full tunnel", []*namedTunnel{full, shell}, []string{cmd}, shell},
		{"domain overrides inherited rule", []*namedTunnel{shell, domain}, []string{cmd}, domain},
		{"unrelated child stays direct", []*namedTunnel{shell}, []string{`C:\other.exe`}, nil},
		{"unknown owner stays direct", []*namedTunnel{shell}, nil, nil},
		{"full tunnel fallback", []*namedTunnel{shell, full}, nil, full},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bridge{tunnels: tt.tunnels}
			if got := b.pickTunnel(curl, "203.0.113.1", tt.ancestors...); got != tt.want {
				t.Fatalf("pickTunnel = %v, want %v", got, tt.want)
			}
		})
	}
}
