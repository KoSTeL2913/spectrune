// Backs the Settings panel's "service isn't running, start it?" prompt
// (webui_html.go) — the GUI is a separate, non-elevated process from
// SpectruneService (talks to it only over the named pipe, see ipc.go),
// so it has no way to notice the service is down other than asking SCM
// directly; and no way to start it itself, since starting a LocalSystem
// service needs the caller's token to actually be elevated (query/read
// rights are open to any authenticated user by default, but
// SERVICE_START is Administrators-only under the plain SCM ACL this
// service was installed with — see installService's mgr.Config, no
// custom SDDL). Starting it needs a UAC prompt via ShellExecute's
// "runas" verb rather than calling mgr.Service.Start() directly from
// this process.
//
// serviceRunningUnprivileged does NOT reuse activeconfig_windows.go's
// isServiceRunning, despite it doing the exact same query for a
// caller-given name — golang.org/x/sys/windows/svc/mgr's Connect()
// always requests SC_MANAGER_ALL_ACCESS on the SCM handle, which is
// Administrators-only and fails outright ("Access is denied") for the
// plain authenticated-user token this GUI process runs with. That
// helper was only ever exercised from the LocalSystem-token service
// process before (watchMainTunnel); confirmed live 2026-09-18 that
// reusing it from here made every single poll below silently report
// "not running" even while `sc.exe query` in parallel showed the
// service genuinely RUNNING seconds after starting it — every failure
// folds into a bare `false`, with no way to tell "definitely stopped"
// apart from "couldn't even ask". Goes straight to
// windows.OpenSCManager/OpenService instead, requesting only
// SC_MANAGER_CONNECT/SERVICE_QUERY_STATUS — both available to any
// authenticated user by default — which is all a read-only status
// check ever needed in the first place.
package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func serviceRunningUnprivileged(name string) bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)

	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	s, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(s)

	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &status); err != nil {
		return false
	}
	return status.CurrentState == windows.SERVICE_RUNNING
}

// startServiceElevated triggers a UAC prompt for `sc.exe start
// SpectruneService` and then polls serviceRunningUnprivileged for a few
// seconds, since ShellExecute's "runas" launch is fire-and-forget — it
// returns as soon as the elevated process is *launched*, not once
// sc.exe actually finishes starting the service, so the caller can't
// just trust a nil error here to mean success.
func startServiceElevated() (bool, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString("sc.exe")
	args, _ := windows.UTF16PtrFromString("start " + serviceName)
	if err := windows.ShellExecute(0, verb, file, args, nil, windows.SW_HIDE); err != nil {
		return false, fmt.Errorf("ShellExecute: %w", err)
	}

	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		if serviceRunningUnprivileged(serviceName) {
			return true, nil
		}
	}
	return false, nil
}
