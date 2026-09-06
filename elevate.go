// Self-elevation to LocalSystem — needed only to decrypt the main
// AmneziaWG app's DPAPI-protected tunnel configs (encrypted by its
// Manager service, which runs as LocalSystem; DPAPI keys are tied to
// that specific account, Administrator is not enough). Not needed at all
// when running against a manually-edited config.conf.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

const localSystemSID = "S-1-5-18"

func isRunningAsSystem() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	sidStr := user.User.Sid.String()
	return sidStr == localSystemSID
}

// relaunchAsSystem creates a one-shot scheduled task that re-runs this
// same exe (with the same arguments) as SYSTEM, in the current
// interactive session (so its console is visible), then returns — the
// caller should exit right after.
func relaunchAsSystem() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	shortPath, err := windows.UTF16FromString(exe)
	if err != nil {
		return err
	}
	buf := make([]uint16, 260)
	n, err := windows.GetShortPathName(&shortPath[0], &buf[0], uint32(len(buf)))
	target := exe
	if err == nil && n > 0 {
		target = windows.UTF16ToString(buf[:n])
	}

	const taskName = "SpectruneSystemRelaunch"
	_ = runCmd("schtasks", "/delete", "/tn", taskName, "/f")
	if err := runCmd("schtasks", "/create", "/tn", taskName, "/tr", target,
		"/sc", "once", "/st", "23:59", "/ru", "SYSTEM", "/rl", "highest", "/it", "/f"); err != nil {
		return fmt.Errorf("schtasks /create: %w", err)
	}
	if err := runCmd("schtasks", "/run", "/tn", taskName); err != nil {
		return fmt.Errorf("schtasks /run: %w", err)
	}
	log.Printf("relaunched as SYSTEM via scheduled task %q — a new console window should appear; you can close this one", taskName)
	return nil
}

func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, out)
	}
	return nil
}
