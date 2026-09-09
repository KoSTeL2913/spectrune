// Domain-based routing for Linux: unlike app-based routing (cgroup +
// fwmark, routing_linux.go), a domain has no process to tag — any app on
// the system might be the one resolving/using it. So instead: passively
// sniff DNS *responses* system-wide (a raw AF_PACKET socket, not a
// redirect/proxy — redirecting DNS was already tried once for a
// different reason and broke things via a loopback-NAT kernel quirk, see
// routing_linux.go's history; passive sniffing sidesteps NAT entirely),
// and when a response's queried name matches one of the profile's
// configured domains, add every resolved IP to an ipset. A single
// iptables mangle rule marks any packet whose destination is in that
// ipset, regardless of which process sent it — the same fwmark and
// route table app-based routing already uses, so both mechanisms tunnel
// through the exact same tunnel with no interaction needed between them.
package main

import (
	"encoding/binary"
	"log"
	"net"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/net/dns/dnsmessage"
)

const domainSetName = "spectrune_domains"

// htons/etaddr helpers for the raw socket setup below.
func htons(i uint16) uint16 {
	return (i<<8)&0xff00 | i>>8
}

// domainSniffer owns the raw socket + the live set of domains to watch.
type domainSniffer struct {
	mu      sync.Mutex
	domains []string // lower-cased, no trailing dot

	stop chan struct{}
	fd   int
}

func newDomainSniffer() *domainSniffer {
	return &domainSniffer{}
}

func (d *domainSniffer) setDomains(domains []string) {
	normalized := make([]string, 0, len(domains))
	for _, dm := range domains {
		dm = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(dm), "."))
		if dm != "" {
			normalized = append(normalized, dm)
		}
	}
	d.mu.Lock()
	d.domains = normalized
	d.mu.Unlock()
}

// matchingDomain returns the configured domain queried matches (itself
// or a subdomain of it), or "" if none do.
func (d *domainSniffer) matchingDomain(queried string) string {
	queried = strings.ToLower(strings.TrimSuffix(queried, "."))
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dm := range d.domains {
		if queried == dm || strings.HasSuffix(queried, "."+dm) {
			return dm
		}
	}
	return ""
}

// start opens a cooked (no link-layer header) AF_PACKET socket bound to
// ETH_P_IP across every interface and reads IPv4/UDP/DNS-response
// packets from it until stop fires. Needs CAP_NET_RAW — fine, this is
// the root daemon.
func (d *domainSniffer) start() error {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, int(htons(syscall.ETH_P_IP)))
	if err != nil {
		return err
	}
	d.fd = fd
	d.stop = make(chan struct{})
	go d.readLoop()
	return nil
}

func (d *domainSniffer) close() {
	if d.stop != nil {
		close(d.stop)
	}
	if d.fd != 0 {
		syscall.Close(d.fd)
	}
}

func (d *domainSniffer) readLoop() {
	buf := make([]byte, 65536)
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		n, _, err := syscall.Recvfrom(d.fd, buf, 0)
		if err != nil {
			select {
			case <-d.stop:
				return
			default:
				continue
			}
		}
		d.handlePacket(buf[:n])
	}
}

// handlePacket parses a cooked AF_PACKET frame: starts directly at the
// IPv4 header (SOCK_DGRAM AF_PACKET strips the link-layer header for us).
func (d *domainSniffer) handlePacket(pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return // not IPv4 (or too short) — AF_PACKET/ETH_P_IP shouldn't hand us anything else, but be defensive
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+8 || pkt[9] != syscall.IPPROTO_UDP {
		return
	}
	udp := pkt[ihl:]
	srcPort := binary.BigEndian.Uint16(udp[0:2])
	if srcPort != 53 {
		return // only interested in responses *from* a DNS server
	}
	udpLen := int(binary.BigEndian.Uint16(udp[4:6]))
	if udpLen < 8 || ihl+udpLen > len(pkt) {
		return
	}
	dnsPayload := udp[8:udpLen]

	var parser dnsmessage.Parser
	if _, err := parser.Start(dnsPayload); err != nil {
		return
	}
	questions, err := parser.AllQuestions()
	if err != nil || len(questions) == 0 {
		return
	}
	queried := questions[0].Name.String()
	matched := d.matchingDomain(queried)
	if matched == "" {
		return
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return
	}
	answers, err := parser.AllAnswers()
	if err != nil {
		return
	}
	for _, a := range answers {
		// A records only: spectrune_domains is an IPv4-only ipset (hash:ip,
		// family inet) because routing_linux.go's whole policy-routing setup
		// (ip rule/ip route table 100) is IPv4-only too — no ip6tables/`ip
		// -6` counterpart exists yet for the cgroup mechanism either, so an
		// AAAA answer has nowhere valid to route to regardless. Handling it
		// here would just mean a same ipset add failing every time.
		if a.Header.Type != dnsmessage.TypeA {
			continue
		}
		r := a.Body.(*dnsmessage.AResource)
		ip := net.IP(r.A[:])
		if err := runCmd("ipset", "add", domainSetName, ip.String(), "-exist"); err != nil {
			log.Printf("domainSniffer: ipset add %s: %v", ip, err)
			continue
		}
		log.Printf("Spectrune: domain %q (matched list entry %q) -> %s added to tunnel", queried, matched, ip)
	}
}
