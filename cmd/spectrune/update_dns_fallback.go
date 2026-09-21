// Fallback path for the self-update HTTP calls (both platforms' update_*.go)
// when the OS's own DNS/hosts resolution for the request's hostname is
// broken. Confirmed live 2026-09-15 on a real user machine: a stale
// malware-cleanup hosts file pointed api.github.com at a blackhole IP,
// silently breaking every update check with no visible symptom besides a
// generic dial/timeout error buried in service.log — the user had no way to
// notice or fix it themselves. client.Do's normal DNS path (which on
// Windows goes through the OS resolver and therefore the hosts file) can't
// see past that, so this retries via DNS-over-HTTPS against a hardcoded
// IP that never touches the local resolver or hosts file.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// dohResolvers is tried in order until one resolves the hostname — a
// single hardcoded resolver is itself a single point of failure.
// Confirmed live 2026-09-21: a machine hitting exactly this fallback
// path for real (a hosts-poisoned api.github.com) could not reach
// 1.1.1.1 at all — separately from the hosts poisoning, this specific
// network blocks direct IP access to well-known public DNS resolvers
// outright (confirmed live: 1.1.1.1, 8.8.8.8, 9.9.9.9, and 1.0.0.1 all
// refused at the TCP level, and even a normal OS-resolved HTTPS
// request to cloudflare-dns.com's real CDN IP connected fine at TCP
// but hung forever right after the TLS ClientHello — SNI-based
// filtering, not a hosts/DNS-level block at all). A second resolver
// doesn't fix that specific network, but it's still a real
// improvement for the — almost certainly far more common — case this
// was actually built for: a hosts file (or a compromised local
// resolver) poisoning specific target domains without also SNI/IP-
// blocking unrelated DoH providers outright. Each entry is dialed as a
// literal IP, never as a hostname — that's the entire point, since a
// hostname lookup would go right back through the same (potentially
// poisoned) OS resolver this exists to bypass. path/accept differ per
// provider: Cloudflare's JSON API lives at /dns-query and needs the
// Accept header to pick JSON over the RFC 8484 binary wire format;
// Google's is JSON-by-default at the differently-named /resolve and
// doesn't use that header at all. A third provider (Quad9 was
// considered) was left out rather than guessed at — Quad9's DoH
// endpoint is documented for the RFC 8484 wire format, not this
// convenience JSON shape, and shipping a wrong URL here would fail
// silently (just another skipped resolver), which is low-risk but
// still not worth guessing.
type dohProvider struct {
	ip         string
	host       string
	path       string
	jsonAccept bool
}

var dohResolvers = []dohProvider{
	{"1.1.1.1", "cloudflare-dns.com", "/dns-query", true},
	{"8.8.8.8", "dns.google", "/resolve", false},
}

// doWithHostsFallback performs req and, if that fails outright (a dial/DNS
// error — not a real HTTP response with a non-2xx status, which is a
// separate, legitimate outcome the caller already handles), retries it by
// resolving req.URL.Hostname() via DNS-over-HTTPS (trying each of
// dohResolvers in turn) and dialing that resolved IP directly, bypassing
// the OS resolver and hosts file entirely. TLS server name / cert
// validation and the Host header still use the original hostname, so this
// is transparent to the server.
func doWithHostsFallback(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err == nil {
		return resp, nil
	}

	var ip string
	var dohErr error
	for _, resolver := range dohResolvers {
		ip, dohErr = resolveViaDoH(req.Context(), resolver, req.URL.Hostname())
		if dohErr == nil {
			break
		}
	}
	if dohErr != nil {
		return nil, err
	}

	fallbackClient := *client
	fallbackClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				port = "443"
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
	}
	return fallbackClient.Do(req.Clone(req.Context()))
}

type dohResponse struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// resolveViaDoH looks up host's A record through provider, always dialing
// its literal IP so the OS's own resolver and hosts file are never
// consulted for this lookup.
func resolveViaDoH(ctx context.Context, provider dohProvider, host string) (string, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				port = "443"
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(provider.ip, port))
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+provider.host+provider.path+"?name="+host+"&type=A", nil)
	if err != nil {
		return "", err
	}
	if provider.jsonAccept {
		req.Header.Set("Accept", "application/dns-json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var parsed dohResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	for _, a := range parsed.Answer {
		if a.Type == 1 { // A record
			return a.Data, nil
		}
	}
	return "", fmt.Errorf("no A record for %s via DoH at %s", host, provider.host)
}
