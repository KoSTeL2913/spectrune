package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modiphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPidAll = 5
	afInet              = 2
)

// mibTcpRowOwnerPid mirrors MIB_TCPROW_OWNER_PID. Ports are stored in
// network byte order in the low 16 bits of the DWORD (confirmed against
// the Win32 header — a common gotcha).
type mibTcpRowOwnerPid struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

// The TCP table is refreshed at most this often — a single page load opens
// dozens of parallel connections, and re-enumerating the *entire* system's
// TCP table (GetExtendedTcpTable walks every connection on the box) for
// each one individually was the actual cause of the noticeable slowdown
// the user hit; caching briefly turns "one syscall per connection" into
// "one syscall per burst".
const tcpTableCacheTTL = 200 * time.Millisecond

var tcpTableCache struct {
	sync.Mutex
	buf     []byte
	fetched time.Time
}

func fetchTCPTable() ([]byte, error) {
	tcpTableCache.Lock()
	defer tcpTableCache.Unlock()
	if time.Since(tcpTableCache.fetched) < tcpTableCacheTTL && tcpTableCache.buf != nil {
		return tcpTableCache.buf, nil
	}

	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidAll, 0)

	buf := make([]byte, size)
	ret, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, // bOrder
		afInet,
		tcpTableOwnerPidAll,
		0,
	)
	if ret != 0 {
		return nil, fmt.Errorf("GetExtendedTcpTable failed: %d", ret)
	}
	tcpTableCache.buf = buf
	tcpTableCache.fetched = time.Now()
	return buf, nil
}

// findTCPOwnerPID returns the PID owning the given local (IPv4) address:port
// in the system's TCP connection table, using GetExtendedTcpTable —
// standard, no-driver-needed Win32 API (the same one netstat -b/Task
// Manager use to show "which process owns this connection").
// ownerLookupRetries/-Delay compensate for a real timing race: we intercept
// the app's connection at the earliest possible point (gVisor's forwarder
// creating the endpoint), which can run before Windows' own TCP/UDP table
// has registered the new socket yet. Without this, the owning PID lookup
// intermittently comes back empty for a connection that's actually the
// tunneled app's — silently misrouting that one connection to "direct"
// instead of "TUNNEL" while its siblings correctly match, splitting a
// single app's traffic across two different source IPs. Confirmed as the
// cause of Discord's gateway connection flapping/reconnect-looping
// 2026-09-02 (one of several concurrent Discord.exe connections came back
// pid=0 and went direct while the rest went through the tunnel). A few
// retries a couple milliseconds apart costs nothing on the hot path but
// closes the race.
const (
	ownerLookupRetries = 4
	ownerLookupDelay   = 3 * time.Millisecond
)

func findTCPOwnerPID(localPort uint16) (uint32, error) {
	var lastErr error
	for attempt := 0; attempt < ownerLookupRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(ownerLookupDelay)
		}
		pid, err := findTCPOwnerPIDOnce(localPort)
		if err == nil {
			return pid, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func findTCPOwnerPIDOnce(localPort uint16) (uint32, error) {
	buf, err := fetchTCPTable()
	if err != nil {
		return 0, err
	}

	numEntries := binary.LittleEndian.Uint32(buf[0:4])
	const rowSize = 24 // sizeof(mibTcpRowOwnerPid)
	wantPort := swapPort(localPort)
	for i := uint32(0); i < numEntries; i++ {
		off := 4 + i*rowSize
		row := (*mibTcpRowOwnerPid)(unsafe.Pointer(&buf[off]))
		if uint32(wantPort) == row.LocalPort {
			return row.OwningPid, nil
		}
	}
	return 0, fmt.Errorf("no owner found for port %d", localPort)
}

func swapPort(p uint16) uint16 {
	return p<<8 | p>>8
}

const udpTableOwnerPid = 1

// mibUdpRowOwnerPid mirrors MIB_UDPROW_OWNER_PID (UDP is connectionless —
// the table is keyed by local address:port only, no remote side).
type mibUdpRowOwnerPid struct {
	LocalAddr uint32
	LocalPort uint32
	OwningPid uint32
}

var (
	procGetExtendedUdpTable = modiphlpapi.NewProc("GetExtendedUdpTable")
	udpTableCache           struct {
		sync.Mutex
		buf     []byte
		fetched time.Time
	}
)

func fetchUDPTable() ([]byte, error) {
	udpTableCache.Lock()
	defer udpTableCache.Unlock()
	if time.Since(udpTableCache.fetched) < tcpTableCacheTTL && udpTableCache.buf != nil {
		return udpTableCache.buf, nil
	}

	var size uint32
	procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, udpTableOwnerPid, 0)

	buf := make([]byte, size)
	ret, _, _ := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		afInet,
		udpTableOwnerPid,
		0,
	)
	if ret != 0 {
		return nil, fmt.Errorf("GetExtendedUdpTable failed: %d", ret)
	}
	udpTableCache.buf = buf
	udpTableCache.fetched = time.Now()
	return buf, nil
}

func findUDPOwnerPID(localPort uint16) (uint32, error) {
	var lastErr error
	for attempt := 0; attempt < ownerLookupRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(ownerLookupDelay)
		}
		pid, err := findUDPOwnerPIDOnce(localPort)
		if err == nil {
			return pid, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func findUDPOwnerPIDOnce(localPort uint16) (uint32, error) {
	buf, err := fetchUDPTable()
	if err != nil {
		return 0, err
	}
	numEntries := binary.LittleEndian.Uint32(buf[0:4])
	const rowSize = 12 // sizeof(mibUdpRowOwnerPid)
	wantPort := swapPort(localPort)
	for i := uint32(0); i < numEntries; i++ {
		off := 4 + i*rowSize
		row := (*mibUdpRowOwnerPid)(unsafe.Pointer(&buf[off]))
		if uint32(wantPort) == row.LocalPort {
			return row.OwningPid, nil
		}
	}
	return 0, fmt.Errorf("no owner found for UDP port %d", localPort)
}

// processPathCache avoids re-resolving the same PID's exe path on every one
// of its many concurrent connections. Short TTL, mainly to bound staleness
// if a PID gets reused by a different process later.
var processPathCache = struct {
	sync.Mutex
	entries map[uint32]processPathCacheEntry
}{entries: make(map[uint32]processPathCacheEntry)}

type processPathCacheEntry struct {
	path    string
	fetched time.Time
}

const processPathCacheTTL = 10 * time.Second

func processPath(pid uint32) (string, error) {
	processPathCache.Lock()
	if e, ok := processPathCache.entries[pid]; ok && time.Since(e.fetched) < processPathCacheTTL {
		processPathCache.Unlock()
		return e.path, nil
	}
	processPathCache.Unlock()

	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	path := windows.UTF16ToString(buf[:size])

	processPathCache.Lock()
	processPathCache.entries[pid] = processPathCacheEntry{path: path, fetched: time.Now()}
	processPathCache.Unlock()
	return path, nil
}

// bestOutboundInterface asks Windows which interface it would currently use
// to reach dst — called BEFORE we install our own default route, so it
// finds the real physical/Wi-Fi interface, not our TUN.
func bestOutboundInterface(dst netip.Addr) (uint32, error) {
	sa := &windows.SockaddrInet4{}
	copy(sa.Addr[:], dst.AsSlice())
	var idx uint32
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0, err
	}
	return idx, nil
}
