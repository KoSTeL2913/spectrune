package main

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"net/netip"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// Keep a UDP client's DNS transaction independent of the lifetime of its
// upstream UDP mapping. TCP also handles large DNS replies without truncation.
func (b *Bridge) relayDNS(local *gonet.UDPConn, nt *namedTunnel, procName, dstIP string, ancestors ...string) {
	id := b.trackConn(procName, dstIP, nt.name, 53, local, ancestors...)
	defer b.untrackConn(id)
	defer local.Close()
	query := make([]byte, 65535)
	for {
		local.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := local.Read(query)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ip := nt.t.dns[0]

		c, err := nt.t.dial(ctx, netip.AddrPortFrom(ip, 53))
		if err != nil {
			cancel()
			log.Printf("DNS via %q: dial %s: %v", nt.name, ip, err)
			continue
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		var header [2]byte
		binary.BigEndian.PutUint16(header[:], uint16(n))
		_, err = c.Write(append(header[:], query[:n]...))
		if err == nil {
			_, err = io.ReadFull(c, header[:])
		}
		var answer []byte
		if err == nil {
			answer = make([]byte, binary.BigEndian.Uint16(header[:]))
			_, err = io.ReadFull(c, answer)
		}
		c.Close()
		cancel()
		if err != nil {
			log.Printf("DNS via %q: exchange %s: %v", nt.name, ip, err)
			continue
		}
		b.observeDNSResponse(answer)
		if _, err = local.Write(answer); err != nil {
			return
		}
	}
}
