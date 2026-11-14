package netstack

import (
	"net/netip"
	"testing"
	"time"
)

func TestCloseUnblocksPendingPackets(t *testing.T) {
	dev, n, err := CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.200.0.2")}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	c, err := n.DialUDPAddrPort(netip.AddrPort{}, netip.MustParseAddrPort("203.0.113.1:53"))
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan struct{})
	go func() { c.Write([]byte("pending")); close(written) }()
	// Leave the TUN unread, as happens once the device's readers stop.
	time.Sleep(20 * time.Millisecond)
	closed := make(chan struct{})
	go func() { dev.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked behind an unread outgoing packet")
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("writer was not released")
	}
	if err := dev.Close(); err != nil {
		t.Fatal(err)
	}
	c.Close()
}
