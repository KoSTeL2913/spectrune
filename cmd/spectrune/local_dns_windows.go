package main

import (
	"context"
	"encoding/binary"
	"errors"
	"golang.org/x/net/dns/dnsmessage"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Use the adapter's own address. Windows DNS Client binds requests to its
// interface; a 127/8 server attached to a Wintun interface can be rejected.
const localDNSAddress = bridgeAddr

type localDNSProxy struct {
	udp      net.PacketConn
	tcp      net.Listener
	exchange func(context.Context, []byte) ([]byte, error)
	observe  func([]byte)
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	mu       sync.Mutex
	clients  map[net.Conn]bool
	closed   bool
}

func newLocalDNSProxy(address string, exchange func(context.Context, []byte) ([]byte, error), observe func([]byte)) (*localDNSProxy, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &localDNSProxy{exchange: exchange, observe: observe, ctx: ctx, cancel: cancel, clients: make(map[net.Conn]bool)}
	var err error
	p.udp, err = net.ListenPacket("udp4", address)
	if err != nil {
		cancel()
		return nil, err
	}
	// Port zero is useful for a self-contained transport test.
	p.tcp, err = net.Listen("tcp4", p.udp.LocalAddr().String())
	if err != nil {
		p.udp.Close()
		cancel()
		return nil, err
	}
	p.workers.Add(2)
	go p.serveUDP()
	go p.serveTCP()
	return p, nil
}
func (p *localDNSProxy) answer(query []byte) []byte {
	ctx, cancel := context.WithTimeout(p.ctx, 6*time.Second)
	defer cancel()
	answer, err := p.exchange(ctx, query)
	if err != nil {
		if len(query) < 12 {
			return nil
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(query); err != nil {
			return nil
		}
		msg.Header.Response = true
		msg.Header.RCode = dnsmessage.RCodeServerFailure
		msg.Header.RecursionAvailable = true
		msg.Answers = nil
		msg.Authorities = nil
		msg.Additionals = nil
		answer, _ = msg.Pack()
		return answer
	}
	if p.observe != nil {
		p.observe(answer)
	}
	return answer
}
func (p *localDNSProxy) serveUDP() {
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
			if answer := p.answer(query); answer != nil {
				p.udp.WriteTo(answer, from)
			}
		}()
	}
}
func (p *localDNSProxy) serveTCP() {
	defer p.workers.Done()
	for {
		c, err := p.tcp.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			c.Close()
			return
		}
		p.clients[c] = true
		p.mu.Unlock()
		p.workers.Add(1)
		go func() {
			defer p.workers.Done()
			defer c.Close()
			defer func() { p.mu.Lock(); delete(p.clients, c); p.mu.Unlock() }()
			for {
				c.SetDeadline(time.Now().Add(30 * time.Second))
				var h [2]byte
				if _, err := io.ReadFull(c, h[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(h[:]))
				if _, err := io.ReadFull(c, query); err != nil {
					return
				}
				answer := p.answer(query)
				if answer == nil {
					return
				}
				binary.BigEndian.PutUint16(h[:], uint16(len(answer)))
				if _, err = c.Write(append(h[:], answer...)); err != nil {
					return
				}
			}
		}()
	}
}
func (p *localDNSProxy) close() {
	p.cancel()
	p.udp.Close()
	p.tcp.Close()
	p.mu.Lock()
	p.closed = true
	for c := range p.clients {
		c.Close()
	}
	p.mu.Unlock()
	p.workers.Wait()
}

func (b *Bridge) exchangeDNS(ctx context.Context, query []byte) ([]byte, error) {
	nt := b.dnsTunnel("")
	if nt == nil {
		return nil, errors.New("no DNS tunnel connected")
	}
	return exchangeTunnelDNS(ctx, nt, query)
}
func exchangeTunnelDNS(ctx context.Context, nt *namedTunnel, query []byte) ([]byte, error) {
	var last error
	for _, ip := range nt.t.dns {
		c, err := nt.t.dial(ctx, netip.AddrPortFrom(ip, 53))
		if err != nil {
			last = err
			continue
		}
		deadline := time.Now().Add(5 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		c.SetDeadline(deadline)
		stopped := context.AfterFunc(ctx, func() { c.Close() })
		var h [2]byte
		binary.BigEndian.PutUint16(h[:], uint16(len(query)))
		_, err = c.Write(append(h[:], query...))
		if err == nil {
			_, err = io.ReadFull(c, h[:])
		}
		var answer []byte
		if err == nil {
			answer = make([]byte, binary.BigEndian.Uint16(h[:]))
			_, err = io.ReadFull(c, answer)
		}
		stopped()
		c.Close()
		if err == nil {
			return answer, nil
		}
		last = err
	}
	return nil, last
}
