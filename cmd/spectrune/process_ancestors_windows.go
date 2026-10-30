package main

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var parentProcessCache struct {
	sync.Mutex
	parents map[uint32]uint32
	fetched time.Time
}

// Cache snapshots during connection bursts, but refresh for a newly launched
// child absent from the snapshot. Paths and creation times are read live.
func processParents(pid uint32) map[uint32]uint32 {
	parentProcessCache.Lock()
	defer parentProcessCache.Unlock()
	if _, ok := parentProcessCache.parents[pid]; ok && time.Since(parentProcessCache.fetched) < tcpTableCacheTTL {
		return parentProcessCache.parents
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	parents := make(map[uint32]uint32)
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil
	}
	parentProcessCache.parents = parents
	parentProcessCache.fetched = time.Now()
	return parents
}

func processIdentity(pid uint32) (string, uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", 0, err
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return "", 0, err
	}
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err = windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", 0, err
	}
	return windows.UTF16ToString(buf[:size]), uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

// A shell's sockets belong to children such as curl.exe, not cmd.exe.
// Only follow living parents older than their children: Windows can reuse
// an exited parent's PID for an unrelated process.
func processAncestorPaths(pid uint32) []string {
	if pid == 0 {
		return nil
	}
	parents := processParents(pid)
	_, childCreated, err := processIdentity(pid)
	if err != nil {
		return nil
	}
	seen := map[uint32]bool{pid: true}
	var paths []string
	for len(paths) < 64 {
		parent := parents[pid]
		if parent == 0 || seen[parent] {
			break
		}
		seen[parent] = true
		path, created, err := processIdentity(parent)
		if err != nil || created > childCreated {
			break
		}
		paths = append(paths, path)
		pid, childCreated = parent, created
	}
	return paths
}
