package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestLocalDNSTransportsAndShutdown(t *testing.T) {
	name, _ := dnsmessage.NewName("example.test.")
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 123, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	query, _ := q.Pack()
	var observed atomic.Int32
	p, err := newLocalDNSProxy("127.0.0.1:0", func(_ context.Context, data []byte) ([]byte, error) {
		var m dnsmessage.Message
		if err := m.Unpack(data); err != nil {
			return nil, err
		}
		m.Header.Response = true
		return m.Pack()
	}, func([]byte) { observed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.Dial("udp4", p.udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udp.SetDeadline(time.Now().Add(time.Second))
	udp.Write(query)
	buf := make([]byte, 512)
	if _, err = udp.Read(buf); err != nil {
		t.Fatal(err)
	}
	if observed.Load() != 1 {
		t.Fatal("DNS response delivered before route observation")
	}
	tcp, err := net.Dial("tcp4", p.tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	tcp.SetDeadline(time.Now().Add(time.Second))
	var h [2]byte
	binary.BigEndian.PutUint16(h[:], uint16(len(query)))
	tcp.Write(h[:1])
	tcp.Write(append(h[1:], query...))
	if _, err = io.ReadFull(tcp, h[:]); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, binary.BigEndian.Uint16(h[:]))
	if _, err = io.ReadFull(tcp, answer); err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err = m.Unpack(answer); err != nil || m.Header.ID != 123 || !m.Header.Response {
		t.Fatalf("invalid DNS response: %v %+v", err, m.Header)
	}
	done := make(chan struct{})
	go func() { p.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle TCP client blocked DNS shutdown")
	}
}
func TestLocalDNSUpstreamFailure(t *testing.T) {
	name, _ := dnsmessage.NewName("example.test.")
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: 456}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	query, _ := m.Pack()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := localDNSProxy{ctx: ctx, exchange: func(context.Context, []byte) ([]byte, error) { return nil, errors.New("upstream unavailable") }}
	answer := p.answer(query)
	if err := m.Unpack(answer); err != nil {
		t.Fatal(err)
	}
	if m.Header.ID != 456 || m.Header.RCode != dnsmessage.RCodeServerFailure || !m.Header.Response {
		t.Fatal("upstream failure was not returned as DNS SERVFAIL")
	}
}
