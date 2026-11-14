package main

import (
	"golang.org/x/net/dns/dnsmessage"
	"net/netip"
	"testing"
)

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

func TestLiveDomainRemovalRetainsOtherList(t *testing.T) {
	tunnel := &outboundTunnel{domains: []string{"example.com", "other.test"}, domainListCount: 2, domainAnswers: map[string][]string{"api.example.com": {"203.0.113.1"}, "other.test": {"203.0.113.2"}}}
	tunnel.updateDomains([]string{"example.com", "other.test"})
	if !tunnel.matchesDomainIP("203.0.113.1") {
		t.Fatal("missing initial answer")
	}
	tunnel.updateSelection([]string{`C:\curl.exe`}, []string{"other.test"}, 1)
	if tunnel.matchesDomainIP("203.0.113.1") || !tunnel.matchesDomainIP("203.0.113.2") {
		t.Fatal("routing did not follow live domain selection")
	}
}

func TestDNSConnectionsSurviveSelectionRefresh(t *testing.T) {
	nt := &namedTunnel{name: "split", t: &outboundTunnel{includedApps: buildIncludedApps([]string{`C:\curl.exe`}), dns: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}}
	b := &Bridge{tunnels: []*namedTunnel{nt}}
	if got := b.pickConnectionTunnel(`C:\svchost.exe`, "1.1.1.1", 53); got != nt {
		t.Fatal("configured DNS was not routed through its tunnel")
	}
	if got := b.pickConnectionTunnel(`C:\svchost.exe`, "1.1.1.1", 443); got != nil {
		t.Fatal("non-DNS traffic was incorrectly included")
	}
	closed := false
	b.trackConn(`C:\svchost.exe`, "1.1.1.1", "split", 53, closeFunc(func() error { closed = true; return nil }))
	b.closeStaleConns()
	if closed {
		t.Fatal("routing refresh closed an unchanged DNS connection")
	}
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

func TestFullTunnelSelectionConflict(t *testing.T) {
	b := &Bridge{tunnels: []*namedTunnel{{name: "full", t: &outboundTunnel{}}, {name: "domain", t: &outboundTunnel{domainListCount: 1}}}}
	if b.otherFullTunnel("full") != "" {
		t.Fatal("profile conflicted with itself")
	}
	if got := b.otherFullTunnel("domain"); got != "full" {
		t.Fatalf("full tunnel conflict=%q", got)
	}
}

func TestDomainRouteBeatsApplicationAcrossThreeConnections(t *testing.T) {
	app := `C:\browser.exe`
	first := &namedTunnel{name: "first", t: &outboundTunnel{includedApps: buildIncludedApps([]string{app})}}
	second := &namedTunnel{name: "second", t: &outboundTunnel{domainListCount: 1}}
	third := &namedTunnel{name: "third", t: &outboundTunnel{domainListCount: 1, domainIPs: map[string]bool{"203.0.113.1": true}}}
	b := &Bridge{tunnels: []*namedTunnel{first, second, third}}
	if b.pickTunnel(app, "203.0.113.1") != third {
		t.Fatal("domain route lost to earlier app selection")
	}
	if b.pickTunnel(app, "203.0.113.2") != first {
		t.Fatal("unrelated app route changed")
	}
	third.t.updateDomains(nil)
	second.t.domainAnswers = map[string][]string{"example.com": {"203.0.113.1"}}
	second.t.updateDomains([]string{"example.com"})
	if b.pickTunnel(app, "203.0.113.1") != second {
		t.Fatal("live domain transfer retained old route")
	}
	full := &outboundTunnel{}
	full.updateDomains([]string{"example.com"})
	if !full.isFullTunnel() {
		t.Fatal("assigning domains changed full tunnel mode")
	}
}

func TestAssignedDomainBeatsOverlappingList(t *testing.T) {
	first := &namedTunnel{name: "first", t: &outboundTunnel{domainListCount: 1, domainIPs: map[string]bool{"203.0.113.1": true}}}
	owner := &namedTunnel{name: "owner", t: &outboundTunnel{domainListCount: 1, priorityDomains: []string{"example.com"}, domainAnswers: map[string][]string{"example.com": {"203.0.113.1"}}}}
	owner.t.updateDomains([]string{"example.com"})
	b := &Bridge{tunnels: []*namedTunnel{first, owner}}
	if b.pickTunnel("", "203.0.113.1") != owner {
		t.Fatal("earlier list overrode explicitly assigned route")
	}
}

func TestAAAAResponseRetainsIPv4DomainRoute(t *testing.T) {
	name, _ := dnsmessage.NewName("example.com.")
	tunnel := &outboundTunnel{domains: []string{"example.com"}, domainListCount: 1, domainIPs: map[string]bool{}}
	a := dnsmessage.Message{Header: dnsmessage.Header{Response: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 1}}}}}
	payload, _ := a.Pack()
	tunnel.observeDNSResponse(payload)
	a.Questions[0].Type = dnsmessage.TypeAAAA
	a.Answers = nil
	payload, _ = a.Pack()
	tunnel.observeDNSResponse(payload)
	if !tunnel.matchesDomainIP("203.0.113.1") {
		t.Fatal("AAAA response removed a cached IPv4 route")
	}
}
