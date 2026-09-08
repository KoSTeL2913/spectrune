// IPC between the Spectrune root daemon and any client (CLI subcommands,
// later the GUI) — the Linux analog of ipc_windows.go's named pipe, same
// net/rpc (gob-encoded) protocol, just over a Unix domain socket instead.
// Trust model: the socket is 0660, owned root:spectrune — any member of
// the "spectrune" system group can issue Connect/Disconnect/Status/etc.
// without a password prompt (same as the Windows named pipe's SDDL
// granting Authenticated Users read/write), which matters because the
// GUI/tray poll State every 2-3 seconds — a polkit prompt per call would
// be unusable. Only the daemon itself needs root (to create the group,
// bind the socket, and actually enter namespaces).
package main

import (
	"fmt"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"os/user"
)

const socketDir = "/run/spectrune"
const socketPath = socketDir + "/control.sock"
const spectruneGroup = "spectrune"

// ipcListen opens the control socket for the daemon side. Creates the
// "spectrune" system group if it doesn't exist yet (normally a package
// postinst's job — done here too so the daemon is self-sufficient even
// run outside a real package install, e.g. during development) and adds
// the invoking SUDO_USER to it so a fresh install doesn't need a re-login
// before the group membership takes effect for a *different* shell, only
// for the one that ran the install.
func ipcListen() (net.Listener, error) {
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		return nil, fmt.Errorf("MkdirAll %s: %w", socketDir, err)
	}
	os.Remove(socketPath) // stale socket from an unclean shutdown

	if _, err := user.LookupGroup(spectruneGroup); err != nil {
		if out, err := exec.Command("groupadd", "-r", spectruneGroup).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("groupadd %s: %w (%s)", spectruneGroup, err, out)
		}
	}
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		exec.Command("usermod", "-aG", spectruneGroup, sudoUser).Run() // best-effort
	}

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("net.Listen(unix, %s): %w", socketPath, err)
	}

	grp, err := user.LookupGroup(spectruneGroup)
	if err == nil {
		var gid int
		fmt.Sscanf(grp.Gid, "%d", &gid)
		os.Chown(socketPath, -1, gid)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		l.Close()
		return nil, fmt.Errorf("chmod %s: %w", socketPath, err)
	}
	return l, nil
}

// ipcDial connects to the control socket as a client (CLI subcommand or
// GUI). Fails fast if the daemon isn't running or the caller lacks
// permission (not in the "spectrune" group).
func ipcDial() (*rpc.Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to Spectrune daemon (is spectruned running? are you in the %q group?): %w", spectruneGroup, err)
	}
	return rpc.NewClient(conn), nil
}

// serveIPC accepts connections on l forever, dispatching each to svc's
// registered RPC methods — same shape as ipc_windows.go's serveIPC.
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
