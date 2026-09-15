// Fallback path for the self-update HTTP calls (both platforms' update_*.go)
// when the OS's own DNS/hosts resolution for the request's hostname is
// broken. Confirmed live 2026-09-15 on a real user machine: a stale
// malware-cleanup hosts file pointed api.github.com at a blackhole IP,
// silently breaking every update check with no visible symptom besides a
// generic dial/timeout error buried in service.log — the user had no way to
// notice or fix it themselves. client.Do's normal DNS path (which on
// Windows goes through the OS resolver and therefore the hosts file) can't
// see past that, so this retries once via DNS-over-HTTPS against a
// hardcoded IP that never touches the local resolver or hosts file.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// dohResolverIP is dialed as a literal IP, never as a hostname — that's the
// entire point, since a hostname lookup would go right back through the
// same (potentially poisoned) OS resolver this exists to bypass.
const dohResolverIP = "1.1.1.1"

// doWithHostsFallback performs req and, if that fails outright (a dial/DNS
// error — not a real HTTP response with a non-2xx status, which is a
// separate, legitimate outcome the caller already handles), retries it once
// by resolving req.URL.Hostname() via DNS-over-HTTPS and dialing that
// resolved IP directly, bypassing the OS resolver and hosts file entirely
// for this one retry. TLS server name / cert validation and the Host header
// still use the original hostname, so this is transparent to the server.
func doWithHostsFallback(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err == nil {
		return resp, nil
	}

	ip, dohErr := resolveViaDoH(req.Context(), req.URL.Hostname())
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

// resolveViaDoH looks up host's A record through Cloudflare's DNS-over-HTTPS
// JSON API, always dialing dohResolverIP as a literal IP so the OS's own
// resolver and hosts file are never consulted for this lookup.
func resolveViaDoH(ctx context.Context, host string) (string, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				port = "443"
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(dohResolverIP, port))
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://cloudflare-dns.com/dns-query?name="+host+"&type=A", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/dns-json")

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
	return "", fmt.Errorf("no A record for %s via DoH", host)
}
