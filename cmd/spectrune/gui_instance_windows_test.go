package main

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestGUIExistingMutexDoesNotKeepOwnerAlive(t *testing.T) {
	name := fmt.Sprintf(`Local\SpectruneGUITest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	owner, existing, err := acquireNamedGUIMutex(name, false)
	if err != nil || existing {
		t.Fatalf("first owner: %v %v", existing, err)
	}
	defer func() {
		if owner != 0 {
			windows.CloseHandle(owner)
		}
	}()
	second, existing, err := acquireNamedGUIMutex(name, false)
	if err != nil || !existing || second != 0 {
		t.Fatalf("duplicate: %v %v %v", second, existing, err)
	}
	done := make(chan windows.Handle, 1)
	go func() {
		h, exists, err := acquireNamedGUIMutex(name, true)
		if err != nil || exists {
			done <- 0
			return
		}
		done <- h
	}()
	time.Sleep(250 * time.Millisecond)
	windows.CloseHandle(owner)
	owner = 0
	select {
	case h := <-done:
		if h == 0 {
			t.Fatal("retry never acquired released mutex")
		}
		windows.CloseHandle(h)
	case <-time.After(2 * time.Second):
		t.Fatal("retry retained its own handles and kept the old mutex alive")
	}
}
func TestGUIMutexUnexpectedErrorFailsClosed(t *testing.T) {
	name := fmt.Sprintf(`Local\SpectruneGUIEvent-%d-%d`, os.Getpid(), time.Now().UnixNano())
	ptr, _ := windows.UTF16PtrFromString(name)
	event, err := windows.CreateEvent(nil, 0, 0, ptr)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	handle, existing, err := acquireNamedGUIMutex(name, false)
	if err == nil || handle != 0 || existing {
		t.Fatalf("event collision must reject GUI creation: %v %v %v", handle, existing, err)
	}
}
