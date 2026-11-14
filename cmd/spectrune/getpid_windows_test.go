package main

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func ipv4DWORD(s string) uint32 { return binary.LittleEndian.Uint32(netip.MustParseAddr(s).AsSlice()) }
func TestTCPProcessLookupUsesFullConnection(t *testing.T) {
	old, when := tcpTableCache.buf, tcpTableCache.fetched
	defer func() { tcpTableCache.buf = old; tcpTableCache.fetched = when }()
	rows := []mibTcpRowOwnerPid{
		{LocalAddr: ipv4DWORD("192.0.2.2"), LocalPort: uint32(swapPort(50000)), RemoteAddr: ipv4DWORD("203.0.113.1"), RemotePort: uint32(swapPort(443)), OwningPid: 100},
		{LocalAddr: ipv4DWORD("10.99.0.1"), LocalPort: uint32(swapPort(50000)), RemoteAddr: ipv4DWORD("203.0.113.2"), RemotePort: uint32(swapPort(443)), OwningPid: 200},
		{LocalAddr: ipv4DWORD("10.99.0.1"), LocalPort: uint32(swapPort(50000)), RemoteAddr: ipv4DWORD("203.0.113.1"), RemotePort: uint32(swapPort(443)), OwningPid: 300},
	}
	buf := make([]byte, 4+24*len(rows))
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, r := range rows {
		for j, v := range []uint32{r.State, r.LocalAddr, r.LocalPort, r.RemoteAddr, r.RemotePort, r.OwningPid} {
			binary.LittleEndian.PutUint32(buf[4+i*24+j*4:], v)
		}
	}
	tcpTableCache.buf = buf
	tcpTableCache.fetched = time.Now()
	pid, err := findTCPOwnerPIDOnce(netip.MustParseAddrPort("10.99.0.1:50000"), netip.MustParseAddrPort("203.0.113.1:443"), false)
	if err != nil || pid != 300 {
		t.Fatalf("PID=%d err=%v; chose another socket sharing the port", pid, err)
	}
}
func TestUDPProcessLookupPrefersExactLocalAddress(t *testing.T) {
	old, when := udpTableCache.buf, udpTableCache.fetched
	defer func() { udpTableCache.buf = old; udpTableCache.fetched = when }()
	rows := []mibUdpRowOwnerPid{{LocalAddr: 0, LocalPort: uint32(swapPort(50000)), OwningPid: 100}, {LocalAddr: ipv4DWORD("192.0.2.2"), LocalPort: uint32(swapPort(50000)), OwningPid: 200}, {LocalAddr: ipv4DWORD("10.99.0.1"), LocalPort: uint32(swapPort(50000)), OwningPid: 300}}
	buf := make([]byte, 4+12*len(rows))
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, r := range rows {
		for j, v := range []uint32{r.LocalAddr, r.LocalPort, r.OwningPid} {
			binary.LittleEndian.PutUint32(buf[4+i*12+j*4:], v)
		}
	}
	udpTableCache.buf = buf
	udpTableCache.fetched = time.Now()
	pid, err := findUDPOwnerPIDOnce(netip.MustParseAddrPort("10.99.0.1:50000"), false)
	if err != nil || pid != 300 {
		t.Fatalf("PID=%d err=%v", pid, err)
	}
	pid, err = findUDPOwnerPIDOnce(netip.MustParseAddrPort("192.0.2.3:50000"), false)
	if err != nil || pid != 100 {
		t.Fatalf("wildcard fallback PID=%d err=%v", pid, err)
	}
}
