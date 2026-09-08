// Spectrune — CLI entry point.
//
// Two generations of usage coexist right now (see
// ~/.claude/plans/idempotent-nibbling-hinton.md):
//
//   - Legacy direct-run (no args): single-instance guard, self-elevate,
//     auto-detect the AmneziaWG app's active tunnel or fall back to
//     config.conf, run until Ctrl+C/the watched tunnel disconnects. This
//     is what the AmneziaWG app's launchAsptBridge() still invokes —
//     unchanged, so that integration keeps working.
//   - Service + IPC (new, Phase 2): /service is the actual SCM entry
//     point (see service.go); /installservice, /uninstallservice set it
//     up; /connect, /disconnect, /status, /list, /saveprofile,
//     /deleteprofile are a thin CLI over the same IPC a future GUI will
//     use (see ipc.go) — both for standalone use today and as the
//     Phase-1/2 verification harness.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	if len(os.Args) > 1 {
		if handled := dispatch(os.Args[1], os.Args[2:]); handled {
			return
		}
	}

	runLegacyDirect()
}

// dispatch handles the new service/IPC subcommands. Returns false for
// anything it doesn't recognize, so the caller falls through to the
// legacy no-args behavior (keeps `spectrune.exe somepath.conf`, the
// legacy positional-config-path form, working unchanged).
func dispatch(cmd string, args []string) bool {
	switch cmd {
	case "/service":
		if err := runService(); err != nil {
			log.Fatalf("runService: %v", err)
		}
	case "/installservice":
		// Hidden the same way /gui hides its own console (webui.go) — the
		// installer's deferred custom actions (installer/spectrune.wxs)
		// invoke this exe directly via CreateProcess, and since it's a
		// plain console-subsystem binary, Windows allocates and flashes a
		// new console window for it on the user's desktop for the
		// duration of the install. There's no way to suppress that
		// allocation from the MSI side (the exe-launch custom action type
		// doesn't expose CREATE_NO_WINDOW), so it's hidden here instead,
		// as early as possible.
		hideConsoleWindow()
		if err := installService(); err != nil {
			log.Fatalf("installService: %v", err)
		}
		fmt.Println("service installed and started")
	case "/uninstallservice":
		hideConsoleWindow()
		if err := uninstallService(); err != nil {
			log.Fatalf("uninstallService: %v", err)
		}
		fmt.Println("service uninstalled")
	case "/connect":
		requireArgs(args, 1, "/connect PROFILE_NAME")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.Connect", args[0], &struct{}{}))
		fmt.Printf("connected: %s\n", args[0])
	case "/disconnect":
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.Disconnect", struct{}{}, &struct{}{}))
		fmt.Println("disconnected")
	case "/status":
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		var reply StateReply
		fatalIf(client.Call("Bridge.State", struct{}{}, &reply))
		if reply.Connected {
			fmt.Printf("connected: %s\n", reply.ProfileName)
		} else {
			fmt.Println("disconnected")
		}
	case "/list":
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		var reply []string
		fatalIf(client.Call("Bridge.ListProfiles", struct{}{}, &reply))
		for _, name := range reply {
			fmt.Println(name)
		}
	case "/saveprofile":
		requireArgs(args, 2, "/saveprofile WGQUICK_FILE PROFILE_NAME")
		data, err := os.ReadFile(args[0])
		fatalIf(err)
		cfg, err := conf.FromWgQuick(string(data), args[1])
		fatalIf(err)
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.SaveProfile", *cfg, &struct{}{}))
		fmt.Printf("saved profile: %s\n", args[1])
	case "/deleteprofile":
		requireArgs(args, 1, "/deleteprofile PROFILE_NAME")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.DeleteProfile", args[0], &struct{}{}))
		fmt.Printf("deleted profile: %s\n", args[0])
	case "/gui":
		runGUI()
	default:
		return false
	}
	return true
}

func requireArgs(args []string, n int, usage string) {
	if len(args) < n {
		log.Fatalf("usage: spectrune.exe %s", usage)
	}
}

func fatalIf(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// runLegacyDirect is the original, pre-service behavior: run one Bridge
// directly in this process until told to stop. Unchanged so the AmneziaWG
// app's existing launchAsptBridge() (which invokes the bare exe with no
// arguments) keeps working exactly as before.
func runLegacyDirect() {
	// Single-instance guard: the main AmneziaWG app auto-launches us on
	// every tunnel connect, and the user can also launch us manually —
	// running twice would fight over the same Wintun adapter name and the
	// system default route. A well-known named mutex is the standard way
	// to detect this cheaply, before touching anything else.
	mutexName, _ := windows.UTF16PtrFromString(`Global\SpectruneSingleInstance`)
	mutex, mutexErr := windows.CreateMutex(nil, false, mutexName)
	if mutexErr == windows.ERROR_ALREADY_EXISTS {
		log.Printf("another Spectrune instance is already running — exiting")
		return
	}
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}

	if !isRunningAsSystem() {
		log.Printf("not running as SYSTEM yet — relaunching to read the active AmneziaWG tunnel's config")
		if elevErr := relaunchAsSystem(); elevErr != nil {
			log.Fatalf("relaunchAsSystem: %v", elevErr)
		}
		return
	}

	cfg, watchedTunnelName := loadLegacyConfig()

	bridge := &Bridge{}
	if err := bridge.Start(cfg); err != nil {
		log.Fatalf("Start: %v", err)
	}

	shutdownCh := make(chan string, 1)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		shutdownCh <- "Ctrl+C"
	}()

	if watchedTunnelName != "" {
		go watchMainTunnel(watchedTunnelName, shutdownCh)
	}

	reason := <-shutdownCh
	log.Printf("shutting down (%s)", reason)
	bridge.Stop()
}

// loadLegacyConfig tries the live AmneziaWG app's active tunnel first,
// then falls back to a config.conf next to the exe (or given as argv[1]).
// Returns a nil *conf.Config for pure pass-through mode if neither is
// available. watchedTunnelName is non-empty only when the live-config path
// succeeded, so runLegacyDirect can watch that specific tunnel for
// disconnects.
func loadLegacyConfig() (cfg *conf.Config, watchedTunnelName string) {
	if activeCfg, tunnelName, activeErr := loadActiveConfig(); activeErr == nil {
		log.Printf("using live config from AmneziaWG app, active tunnel %q", tunnelName)
		return activeCfg, tunnelName
	} else {
		log.Printf("could not auto-detect active AmneziaWG tunnel (%v) — falling back to config.conf", activeErr)
	}

	confPath := "config.conf"
	if len(os.Args) > 1 {
		confPath = os.Args[1]
	} else if exe, exErr := os.Executable(); exErr == nil {
		confPath = filepath.Join(filepath.Dir(exe), "config.conf")
	}
	data, readErr := os.ReadFile(confPath)
	if readErr != nil {
		log.Printf("no config at %s either", confPath)
		return nil, ""
	}
	parsed, parseErr := conf.FromWgQuick(string(data), "spectrune")
	if parseErr != nil {
		log.Printf("parsing %s: %v", confPath, parseErr)
		return nil, ""
	}
	return parsed, ""
}

// watchMainTunnel reacts the instant the main AmneziaWG app connects or
// disconnects any tunnel — it waits on a shared, machine-wide event that
// the (patched) main app's tunnel.Service pulses on every up/down
// transition (see amneziawg-windows/tunnel/statenotify_windows.go). The
// event carries no data — on every wake we just re-check the real service
// state ourselves. A slow poll (30s) runs alongside as a safety net in
// case the main app installed is an older build that doesn't signal the
// event at all, so this degrades gracefully rather than hanging forever.
func watchMainTunnel(name string, shutdownCh chan<- string) {
	amneziaWGServiceName := tunnelServicePrefix + name

	check := func() bool {
		if !isServiceRunning(amneziaWGServiceName) {
			shutdownCh <- fmt.Sprintf("main AmneziaWG tunnel %q disconnected", name)
			return true
		}
		return false
	}

	eventNamePtr, err := windows.UTF16PtrFromString(`Global\SpectruneTunnelStateChanged`)
	var event windows.Handle
	if err == nil {
		event, err = windows.CreateEvent(nil, 1 /* manual reset */, 0, eventNamePtr)
	}
	if err != nil {
		log.Printf("watchMainTunnel: could not open shared state-change event (%v) — falling back to 30s polling only", err)
		for range time.Tick(30 * time.Second) {
			if check() {
				return
			}
		}
	}
	defer windows.CloseHandle(event)

	go func() {
		for range time.Tick(30 * time.Second) {
			if check() {
				return
			}
		}
	}()

	for {
		windows.WaitForSingleObject(event, windows.INFINITE)
		windows.ResetEvent(event)
		if check() {
			return
		}
	}
}
