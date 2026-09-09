// Spectrune — Linux CLI entry point. Mirrors main_windows.go's dispatch
// shape (thin subcommands over the same IPC the future GUI uses) but with
// no legacy no-args mode — Linux Spectrune never had a "companion to the
// official client" phase to stay compatible with.
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	if !dispatch(os.Args[1], os.Args[2:]) {
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`usage: spectrune <command> [args]

commands:
  /service                    run the root daemon (systemd-managed; needs root)
  /connect NAME                connect to a saved profile
  /disconnect                  disconnect the active profile
  /status                      show connection status
  /list                        list saved profile names
  /saveprofile WGQUICK_FILE NAME   save a profile from a wg-quick-style file
  /deleteprofile NAME           delete a saved profile
  /listapps                    list installed .desktop applications
  /launchapp DESKTOP_ID         launch an app inside the tunnel's namespace
  /listdomainlists             list saved domain list names
  /savedomainlist NAME DOMAINS   save a domain list (DOMAINS is comma-separated)
  /loaddomainlist NAME          print a domain list's domains
  /deletedomainlist NAME        delete a domain list
  /gui                         open the GUI window`)
}

func dispatch(cmd string, args []string) bool {
	switch cmd {
	case "/service":
		fatalIf(runDaemon())
	case "/connect":
		requireArgs(args, 1, "/connect NAME")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.Connect", args[0], &struct{}{}))
		// The RPC only means "the namespace/tunnel came up," not "a
		// handshake succeeded" — printing "connected" unconditionally
		// here was misleading (confirmed live 2026-09-08 against a peer
		// whose handshake never completes). Report what /status itself
		// would say instead of a canned success message.
		var reply StateReply
		fatalIf(client.Call("Bridge.State", struct{}{}, &reply))
		if reply.HandshakeOK {
			fmt.Printf("connected: %s\n", args[0])
		} else {
			fmt.Printf("connecting: %s (no handshake yet)\n", args[0])
		}
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
		if !reply.Connected {
			fmt.Println("disconnected")
		} else if reply.HandshakeOK {
			fmt.Printf("connected: %s\n", reply.ProfileName)
		} else {
			fmt.Printf("connecting: %s (no handshake yet)\n", reply.ProfileName)
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
		requireArgs(args, 2, "/saveprofile WGQUICK_FILE NAME")
		data, err := os.ReadFile(args[0])
		fatalIf(err)
		cfg, err := ParseWgQuick(string(data), args[1])
		fatalIf(err)
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.SaveProfile", *cfg, &struct{}{}))
		fmt.Printf("saved profile: %s\n", args[1])
	case "/deleteprofile":
		requireArgs(args, 1, "/deleteprofile NAME")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.DeleteProfile", args[0], &struct{}{}))
		fmt.Printf("deleted profile: %s\n", args[0])
	case "/listapps":
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		var reply []DesktopApp
		fatalIf(client.Call("Bridge.ListApps", struct{}{}, &reply))
		for _, a := range reply {
			fmt.Printf("%s\t%s\n", a.ID, a.Name)
		}
	case "/launchapp":
		requireArgs(args, 1, "/launchapp DESKTOP_ID")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		req := LaunchAppRequest{
			DesktopID: args[0],
			Env: map[string]string{
				"DISPLAY":                  os.Getenv("DISPLAY"),
				"WAYLAND_DISPLAY":          os.Getenv("WAYLAND_DISPLAY"),
				"XDG_RUNTIME_DIR":          os.Getenv("XDG_RUNTIME_DIR"),
				"DBUS_SESSION_BUS_ADDRESS": os.Getenv("DBUS_SESSION_BUS_ADDRESS"),
				"_CALLER_UID":              fmt.Sprint(os.Getuid()),
				"_CALLER_GID":              fmt.Sprint(os.Getgid()),
			},
		}
		fatalIf(client.Call("Bridge.LaunchApp", req, &struct{}{}))
		fmt.Printf("launched: %s\n", args[0])
	case "/listdomainlists":
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		var reply []string
		fatalIf(client.Call("Bridge.ListDomainLists", struct{}{}, &reply))
		for _, name := range reply {
			fmt.Println(name)
		}
	case "/savedomainlist":
		requireArgs(args, 2, "/savedomainlist NAME DOMAINS")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		domains := strings.Split(args[1], ",")
		for i := range domains {
			domains[i] = strings.TrimSpace(domains[i])
		}
		fatalIf(client.Call("Bridge.SaveDomainList", DomainListSaveRequest{Name: args[0], Domains: domains}, &struct{}{}))
		fmt.Printf("saved domain list: %s\n", args[0])
	case "/loaddomainlist":
		requireArgs(args, 1, "/loaddomainlist NAME")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		var domains []string
		fatalIf(client.Call("Bridge.LoadDomainList", args[0], &domains))
		for _, d := range domains {
			fmt.Println(d)
		}
	case "/deletedomainlist":
		requireArgs(args, 1, "/deletedomainlist NAME")
		client, err := ipcDial()
		fatalIf(err)
		defer client.Close()
		fatalIf(client.Call("Bridge.DeleteDomainList", args[0], &struct{}{}))
		fmt.Printf("deleted domain list: %s\n", args[0])
	case "/gui":
		runGUI()
	default:
		return false
	}
	return true
}

func requireArgs(args []string, n int, usage string) {
	if len(args) < n {
		log.Fatalf("usage: spectrune %s", usage)
	}
}

func fatalIf(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
