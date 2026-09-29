package main

import (
	"log"
	"os"

	"golang.org/x/sys/windows"
)

var procAttachConsole = modkernel32.NewProc("AttachConsole")

const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS, (DWORD)-1

// Spectrune is built as a GUI-subsystem exe (-ldflags=-H=windowsgui, see
// installer/spectrune.wxs and README.md), so Windows never allocates a
// console for it. It used to be a console-subsystem exe that hid its own
// console with ShowWindow(GetConsoleWindow(), SW_HIDE) — which works on
// Windows 10's conhost, but not on Windows 11, where the default terminal
// is Windows Terminal: GetConsoleWindow there returns a hidden pseudo
// window, so hiding it does nothing and a Windows Terminal window stayed
// open next to the GUI (and flashed for every service/installer launch).
// Reported 2026-09-29 on a Windows 11 25H2 laptop.
//
// The catch with a GUI-subsystem exe is that the CLI subcommands (/status,
// /list, /connect, …) have no console to print to when run from cmd or
// PowerShell. attachParentConsole fixes that: if our stdout isn't already
// redirected to a pipe/file (e.g. over SSH, or `> out.txt`, both of which
// keep working as-is), attach to the parent process's console, if it has
// one, and point stdout/stderr at it. Launches with no parent console
// (desktop shortcut, autostart, SCM, msiexec) simply stay console-less.
func init() {
	attachParentConsole()
}

func attachParentConsole() {
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		return
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
		// The log package captured the original (invalid) stderr at its
		// own init; the service's later log.SetOutput(service.log) still
		// takes precedence over this.
		log.SetOutput(out)
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = in
	}
}
