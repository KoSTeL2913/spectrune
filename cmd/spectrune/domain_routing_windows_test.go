package main

import "testing"

func TestDomainOnlyRouting(t *testing.T) {
	const app = "C:\\browser.exe"
	domain := &namedTunnel{name: "domain", t: &outboundTunnel{
		domainListCount: 1,
		domains:         []string{"example.com"},
		domainIPs:       map[string]bool{"203.0.113.10": true},
	}}
	full := &namedTunnel{name: "full", t: &outboundTunnel{}}
	for _, tt := range []struct {
		name, ip string
		tunnels  []*namedTunnel
		want     *namedTunnel
	}{
		{"listed destination", "203.0.113.10", []*namedTunnel{domain}, domain},
		{"unrelated destination stays direct", "203.0.113.20", []*namedTunnel{domain}, nil},
		{"domain beats full fallback", "203.0.113.10", []*namedTunnel{full, domain}, domain},
		{"unrelated uses explicit full fallback", "203.0.113.20", []*namedTunnel{domain, full}, full},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bridge{tunnels: tt.tunnels}
			if got := b.pickTunnel(app, tt.ip); got != tt.want {
				t.Fatalf("picked %v, want %v", got, tt.want)
			}
		})
	}
	// Selecting an empty/missing domain list must never widen routing to all apps.
	domain.t.updateSelection(nil, nil, 1)
	if domain.t.isFullTunnel() || domain.t.matches(app) {
		t.Fatal("empty selected domain list enabled full-tunnel routing")
	}
	domain.t.updateSelection(nil, nil, 0)
	if !domain.t.isFullTunnel() || !domain.t.matches(app) {
		t.Fatal("clearing all selectors did not restore full-tunnel routing")
	}
	domain.t.updateSelection([]string{app}, []string{"example.com"}, 1)
	if domain.t.isFullTunnel() || !domain.t.matches(app) || domain.t.matches("C:\\other.exe") {
		t.Fatal("combined app and domain selection has incorrect app matching")
	}
}
