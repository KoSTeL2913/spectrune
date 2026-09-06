// IPC between the Spectrune service (always SYSTEM) and any client —
// today that's the CLI subcommands in main.go, later the GUI from Phase 3.
// net/rpc (stdlib, gob-encoded) over a named pipe from amneziawg-go's own
// ipc/namedpipe package — already a transitive dependency here (used for
// the AmneziaWG UAPI pipe), so this adds no new module.
package main

import (
	"fmt"
	"net"
	"net/rpc"
	"time"

	"golang.org/x/sys/windows"

	"github.com/amnezia-vpn/amneziawg-go/v3/ipc/namedpipe"
)

const pipePath = `\\.\pipe\ProtectedPrefix\Administrators\Spectrune\control`

// pipeSecurityDescriptor grants full access to SYSTEM and Builtin Admins
// (the service always runs as SYSTEM) and generic read/write to
// Authenticated Users — the GUI runs as the logged-in user, not elevated,
// and still needs to connect and issue Connect/Disconnect/List calls.
func pipeSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	return windows.SecurityDescriptorFromString("O:SYD:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)")
}

// ipcListen opens the control pipe for the service side.
func ipcListen() (net.Listener, error) {
	sd, err := pipeSecurityDescriptor()
	if err != nil {
		return nil, fmt.Errorf("SecurityDescriptorFromString: %w", err)
	}
	return (&namedpipe.ListenConfig{SecurityDescriptor: sd}).Listen(pipePath)
}

// ipcDial connects to the control pipe as a client (CLI subcommand or
// GUI). Fails fast (rather than hanging) if the service isn't running.
func ipcDial() (*rpc.Client, error) {
	conn, err := namedpipe.DialTimeout(pipePath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to Spectrune service (is it running? try /installservice): %w", err)
	}
	return rpc.NewClient(conn), nil
}

// serveIPC accepts connections on l forever, dispatching each to svc's
// registered RPC methods. Returns only if Accept itself fails (e.g. the
// listener was closed), which the caller treats as fatal for the service.
func serveIPC(l net.Listener, svc *Service) error {
	server := rpc.NewServer()
	if err := server.RegisterName("Bridge", svc); err != nil {
		return fmt.Errorf("RegisterName: %w", err)
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go server.ServeConn(conn)
	}
}
