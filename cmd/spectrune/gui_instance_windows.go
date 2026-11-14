package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func acquireWindowsGUIMutex(retry bool) (windows.Handle, bool, error) {
	return acquireNamedGUIMutex(`Global\SpectruneGUISingleInstance`, retry)
}

func acquireNamedGUIMutex(mutexName string, retry bool) (windows.Handle, bool, error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return 0, false, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		handle, err := windows.CreateMutex(nil, false, name)
		if err == nil {
			return handle, false, nil
		}
		// CreateMutex returns an open handle on ERROR_ALREADY_EXISTS too.
		// Keeping it while retrying would keep the old mutex alive ourselves.
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		if err == windows.ERROR_ALREADY_EXISTS {
			if retry && time.Now().Before(deadline) {
				time.Sleep(150 * time.Millisecond)
				continue
			}
			return 0, true, nil
		}
		if err == windows.ERROR_ACCESS_DENIED {
			// An elevated instance can own a mutex that the unelevated token
			// cannot reopen. Activate its window rather than creating another.
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("CreateMutex: %w", err)
	}
}
