package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A DNS response must reach ipset before its client opens a connection.
// Passive capture alone races that first connection, sometimes switching
// its route after the SYN. Route selected names through this proxy instead.
type domainDNSProxy struct {
	udp     net.PacketConn
	tcp     net.Listener
	address string
	servers []string
	observe func([]byte)
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	active  map[net.Conn]bool
	closed  bool
	workers sync.WaitGroup
}

func startDomainDNSProxy(address string, servers []string, observe func([]byte)) (*domainDNSProxy, error) {
	ip, _, err := net.ParseCIDR(address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &domainDNSProxy{address: ip.String(), servers: servers, observe: observe, ctx: ctx, cancel: cancel, active: make(map[net.Conn]bool)}
	if len(p.servers) == 0 {
		p.servers = []string{"1.1.1.1"}
	}
	p.udp, err = net.ListenPacket("udp4", net.JoinHostPort(p.address, "53"))
	if err != nil {
		cancel()
		return nil, err
	}
	p.tcp, err = net.Listen("tcp4", net.JoinHostPort(p.address, "53"))
	if err != nil {
		p.udp.Close()
		cancel()
		return nil, err
	}
	p.workers.Add(2)
	go func() {
		defer p.workers.Done()
		buf := make([]byte, 65535)
		for {
			n, from, err := p.udp.ReadFrom(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			p.workers.Add(1)
			go func() {
				defer p.workers.Done()
				if answer, err := p.exchange(query); err == nil {
					p.observe(answer)
					p.udp.WriteTo(answer, from)
				}
			}()
		}
	}()
	go func() {
		defer p.workers.Done()
		for {
			c, err := p.tcp.Accept()
			if err != nil {
				return
			}
			if !p.track(c) {
				c.Close()
				return
			}
			p.workers.Add(1)
			go func() {
				defer p.workers.Done()
				defer c.Close()
				defer p.untrack(c)
				c.SetDeadline(time.Now().Add(6 * time.Second))
				for {
					var header [2]byte
					if _, err := io.ReadFull(c, header[:]); err != nil {
						return
					}
					query := make([]byte, binary.BigEndian.Uint16(header[:]))
					if _, err := io.ReadFull(c, query); err != nil {
						return
					}
					answer, err := p.exchange(query)
					if err != nil {
						return
					}
					p.observe(answer)
					binary.BigEndian.PutUint16(header[:], uint16(len(answer)))
					if _, err = c.Write(append(header[:], answer...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return p, nil
}

func (p *domainDNSProxy) exchange(query []byte) ([]byte, error) {
	var last error
	for _, server := range p.servers {
		d := net.Dialer{Timeout: 2 * time.Second, Control: func(_, _ string, c syscall.RawConn) error {
			var e error
			err := c.Control(func(fd uintptr) { e = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, 0xca11) })
			if err != nil {
				return err
			}
			return e
		}}
		answer, err := p.exchangeServer(&d, "udp4", server, query)
		if err != nil || (len(answer) >= 4 && answer[2]&2 != 0) {
			answer, err = p.exchangeServer(&d, "tcp4", server, query)
		}
		if err == nil {
			return answer, nil
		}
		last = err
	}
	return nil, last
}

func (p *domainDNSProxy) exchangeServer(d *net.Dialer, network, server string, query []byte) ([]byte, error) {
	c, err := d.DialContext(p.ctx, network, net.JoinHostPort(server, "53"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if !p.track(c) {
		return nil, net.ErrClosed
	}
	defer p.untrack(c)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if network == "tcp4" {
		var h [2]byte
		binary.BigEndian.PutUint16(h[:], uint16(len(query)))
		if _, err = c.Write(append(h[:], query...)); err != nil {
			return nil, err
		}
		if _, err = io.ReadFull(c, h[:]); err != nil {
			return nil, err
		}
		answer := make([]byte, binary.BigEndian.Uint16(h[:]))
		_, err = io.ReadFull(c, answer)
		return answer, err
	}
	if _, err = c.Write(query); err != nil {
		return nil, err
	}
	answer := make([]byte, 65535)
	n, err := c.Read(answer)
	return answer[:n], err
}

func (p *domainDNSProxy) setDomains(domains []string) error {
	if len(domains) == 0 {
		runCmdQuiet("resolvectl", "revert", tunName)
		return nil
	}
	if err := runCmd("resolvectl", "dns", tunName, p.address); err != nil {
		return fmt.Errorf("configure domain DNS: %w", err)
	}
	args := []string{"domain", tunName}
	for _, domain := range domains {
		args = append(args, "~"+strings.TrimSuffix(domain, "."))
	}
	if err := runCmd("resolvectl", args...); err != nil {
		return err
	}
	return runCmd("resolvectl", "default-route", tunName, "false")
}
func (p *domainDNSProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.active[c] = true
	return true
}
func (p *domainDNSProxy) untrack(c net.Conn) { p.mu.Lock(); delete(p.active, c); p.mu.Unlock() }
func (p *domainDNSProxy) close() {
	p.cancel()
	p.udp.Close()
	p.tcp.Close()
	p.mu.Lock()
	p.closed = true
	for c := range p.active {
		c.Close()
	}
	p.mu.Unlock()
	p.workers.Wait()
}
