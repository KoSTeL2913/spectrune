// The Spectrune Windows service: owns the one active *Bridge (if any) —
// which carries every connected profile's tunnel at once — and exposes
// it over IPC (see ipc.go) as Connect/Disconnect/DisconnectProfile/State/
// ListProfiles/SaveProfile/LoadProfile/DeleteProfile. Runs as LocalSystem,
// installed once via /installservice — mirrors the AmneziaWGManager
// service's own install pattern (manager/install.go in the client repo),
// scoped down to what Spectrune actually needs: exactly one service, no
// per-tunnel sub-services.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"
)

const serviceName = "SpectruneService"

// Service is the RPC-exposed surface — see ipc.go's serveIPC, which
// registers it under the name "Bridge" (so calls are "Bridge.Connect" etc.
// from the client side).
type Service struct {
	mu     sync.Mutex
	bridge *Bridge // nil while no profile is connected

	// ipv6CleanupDone is closed once the startup IPv6 firewall safety net
	// (winService.Execute) has finished. Set once before IPC starts
	// serving and never reassigned, so reading it needs no lock.
	ipv6CleanupDone chan struct{}
}

type StateReply struct {
	// Connected/ProfileName/HandshakeOK describe the first connected
	// profile only — kept for clients that predate multiple simultaneous
	// connections (an older GUI/tray still running right after a
	// self-update). Tunnels is the full list.
	Connected   bool
	ProfileName string
	// HandshakeOK is false while Connected is true but the WireGuard
	// handshake with the peer hasn't completed yet (or ever will, if the
	// config/server is bad) — see Bridge.HandshakeOK. Connected alone
	// only ever meant "the local adapter came up," which used to read as
	// a false "it's working" in the UI even when the tunnel was
	// completely dead.
	HandshakeOK bool

	// Tunnels lists every connected profile, in connect order.
	Tunnels []TunnelState
}

// Connect connects name alongside whatever is already connected (a
// no-op if name itself already is). Refused when it would clash with a
// connected profile: a second full-tunnel profile (only one can be "the
// rest of the traffic"), or the same WireGuard private key (the server
// can't serve one identity twice — see tunnelPrivateKeyInUse).
func (s *Service) Connect(name string, _ *struct{}) error {
	// Waited on before taking s.mu, so State polling isn't blocked
	// meanwhile.
	if s.ipv6CleanupDone != nil {
		<-s.ipv6CleanupDone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridge != nil && s.bridge.tunnel(name) != nil {
		return nil
	}
	cfg, err := loadProfile(name)
	if err != nil {
		return fmt.Errorf("loadProfile(%q): %w", name, err)
	}
	if s.bridge == nil {
		b, err := startBridge(name, cfg)
		if err != nil {
			return fmt.Errorf("Start: %w", err)
		}
		s.bridge = b
		return nil
	}
	if other := s.bridge.tunnelPrivateKeyInUse(cfg.Interface.PrivateKey); other != "" {
		return fmt.Errorf("profile %q uses the same private key as the connected profile %q — a server can't serve one key twice, give each profile its own key", name, other)
	}
	if app, other := s.bridge.appClaimedBy(cfg.Interface.IncludedApps, name); other != "" {
		return fmt.Errorf("profile %q includes %s, which the connected profile %q already routes — an app can only go through one tunnel at a time", name, app, other)
	}
	if len(cfg.Interface.IncludedApps) == 0 {
		for _, other := range s.bridge.TunnelNames() {
			if s.bridge.tunnel(other).isFullTunnel() {
				return fmt.Errorf("profile %q routes all traffic, and so does the connected profile %q — only one such profile can be connected at a time", name, other)
			}
		}
	}
	if err := s.bridge.AddTunnel(name, cfg); err != nil {
		return fmt.Errorf("AddTunnel: %w", err)
	}
	return nil
}

// DisconnectProfile disconnects just name, leaving other connected
// profiles running; the bridge itself goes down with the last one.
func (s *Service) DisconnectProfile(name string, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridge == nil {
		return nil
	}
	if s.bridge.RemoveTunnel(name) == 0 {
		return s.disconnectLocked()
	}
	return nil
}

// connectedLocked reports whether name is currently connected. Requires s.mu.
func (s *Service) connectedLocked(name string) bool {
	return s.bridge != nil && s.bridge.tunnel(name) != nil
}

// Disconnect disconnects every connected profile.
func (s *Service) Disconnect(_ struct{}, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disconnectLocked()
}

// disconnectLocked requires s.mu already held. Split out so Execute's
// shutdown path can reuse it without a double-lock.
func (s *Service) disconnectLocked() error {
	if s.bridge == nil {
		return nil
	}
	s.bridge.Stop()
	s.bridge = nil
	return nil
}

func (s *Service) State(_ struct{}, reply *StateReply) error {
	s.mu.Lock()
	bridge := s.bridge
	s.mu.Unlock()
	// HandshakeOK's IpcGet() call runs outside the lock — State is polled
	// every 2-3s by both the tray and the GUI, and holding s.mu (the same
	// lock Connect/Disconnect/SaveProfile/DeleteProfile all need) for the
	// duration of a device query neither needs nor benefits from that
	// lock would let frequent polling stall those instead of just this
	// one read.
	if bridge == nil {
		return nil
	}
	reply.Tunnels = bridge.TunnelStates()
	// A bridge momentarily without tunnels (pass-through) still counts as
	// connected for old clients, same as before.
	reply.Connected = true
	reply.HandshakeOK = true
	if len(reply.Tunnels) > 0 {
		reply.ProfileName = reply.Tunnels[0].Name
		reply.HandshakeOK = reply.Tunnels[0].HandshakeOK
	}
	return nil
}

// Version reports the *currently running* service's version — used by
// the "Check for updates" button's post-install poll (webui_html.go) to
// detect when a restarted service has actually come up running the new
// code, since a self-update's own RPC reply can't be trusted to survive
// the restart it triggers (see update_windows.go's CheckForUpdateNow
// doc).
func (s *Service) Version(_ struct{}, reply *string) error {
	*reply = appVersion
	return nil
}

// UpdateInProgress backs the GUI's background poll (webui_html.go's
// checkBackgroundUpdate) — lets it show an honest "installing update"
// overlay instead of a bare connection error while an automatic
// self-update is downloading/installing (update_windows.go's
// updateInProgress flag), including the window where msiexec is about to
// stop and restart this very service.
func (s *Service) UpdateInProgress(_ struct{}, reply *bool) error {
	*reply = updateInProgress.Load()
	return nil
}

func (s *Service) ListProfiles(_ struct{}, reply *[]string) error {
	names, err := listProfileNames()
	if err != nil {
		return err
	}
	*reply = names
	return nil
}

func (s *Service) SaveProfile(cfg conf.Config, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.connectedLocked(cfg.Name)
	if live {
		if app, other := s.bridge.appClaimedBy(cfg.Interface.IncludedApps, cfg.Name); other != "" {
			return fmt.Errorf("%s is already routed by the connected profile %q — an app can only go through one tunnel at a time", app, other)
		}
	}
	if err := saveProfile(&cfg); err != nil {
		return err
	}

	// If this is the profile that's actually connected right now, push
	// the new app selection into the running tunnel immediately instead
	// of leaving it stale until the next Disconnect/Connect — that used
	// to mean tearing down and rebuilding the whole adapter/tunnel (and
	// dropping every other in-flight connection with it) just to change
	// which apps route through it.
	if live {
		s.bridge.UpdateTunnel(cfg.Name, cfg.Interface.IncludedApps, resolveDomainLists(cfg.Interface.IncludedDomainLists))
	}
	return nil
}

// autoConnectOnStartup connects every saved profile with AutoConnect
// enabled — mirrors clicking Connect in the GUI, just triggered once at
// service start instead of by the user. Several can be marked now that
// several can run at once; one that clashes with an earlier one (see
// Connect) just logs the refusal.
// Called from winService.Execute in its own goroutine, right after
// reporting Running to the SCM, since establishing a tunnel can take a
// moment and shouldn't hold up service startup.
func (s *Service) autoConnectOnStartup() {
	names, err := listProfileNames()
	if err != nil {
		log.Printf("autoConnectOnStartup: listProfileNames: %v", err)
		return
	}
	for _, name := range names {
		cfg, err := loadProfile(name)
		if err != nil {
			log.Printf("autoConnectOnStartup: loadProfile(%q): %v", name, err)
			continue
		}
		if !cfg.Interface.AutoConnect {
			continue
		}
		log.Printf("autoConnectOnStartup: connecting to %q", name)
		if err := s.Connect(name, &struct{}{}); err != nil {
			log.Printf("autoConnectOnStartup: Connect(%q): %v", name, err)
		}
	}
}

func (s *Service) LoadProfile(name string, reply *conf.Config) error {
	cfg, err := loadProfile(name)
	if err != nil {
		return err
	}
	*reply = *cfg
	return nil
}

func (s *Service) DeleteProfile(name string, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connectedLocked(name) {
		return fmt.Errorf("profile %q is currently connected — disconnect first", name)
	}
	return deleteProfile(name)
}

// RenameProfileRequest bundles Service.RenameProfile's two arguments —
// net/rpc methods take exactly one request value.
type RenameProfileRequest struct {
	OldName string
	NewName string
}

// RenameProfile moves a saved profile to a new name — refuses while it's
// connected (the bridge's tunnel list, service.log's own
// service.log file, etc. all key off the name; simplest to just require
// Disconnect first rather than trying to rename a live connection out
// from under itself) and refuses to silently clobber a different
// existing profile of the target name. The GUI (webui.go's saveProfile
// binding) calls this before re-saving the edited content under the new
// name, so this only needs to move the file, not merge in the user's
// in-progress edits.
func (s *Service) RenameProfile(req RenameProfileRequest, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connectedLocked(req.OldName) {
		return fmt.Errorf("profile %q is currently connected — disconnect first", req.OldName)
	}
	if !profileNameIsValid(req.NewName) {
		return fmt.Errorf("profile name %q is not valid", req.NewName)
	}
	if req.OldName == req.NewName {
		return nil
	}
	if _, err := loadProfile(req.NewName); err == nil {
		return fmt.Errorf("a profile named %q already exists", req.NewName)
	}
	cfg, err := loadProfile(req.OldName)
	if err != nil {
		return err
	}
	cfg.Name = req.NewName
	if err := saveProfile(cfg); err != nil {
		return err
	}
	return deleteProfile(req.OldName)
}

// App presets (apppresets.go) — named, reusable app-selection lists for
// the Apps picker, unrelated to which tunnel profile is active. Routed
// through the service like everything else in ProgramData\Spectrune
// rather than having the GUI touch that folder directly, same reasoning
// as the profile RPCs above (the service always runs as SYSTEM; relying
// on the GUI process's own, lower-privileged access to a folder under
// ProgramData is exactly the kind of ACL assumption this app avoids
// elsewhere).

func (s *Service) ListAppPresets(_ struct{}, reply *[]string) error {
	names, err := listAppPresetNames()
	if err != nil {
		return err
	}
	*reply = names
	return nil
}

func (s *Service) SaveAppPreset(req AppPresetSaveRequest, _ *struct{}) error {
	return saveAppPreset(req.Name, req.Apps)
}

func (s *Service) LoadAppPreset(name string, reply *[]string) error {
	apps, err := loadAppPreset(name)
	if err != nil {
		return err
	}
	*reply = apps
	return nil
}

func (s *Service) DeleteAppPreset(name string, _ *struct{}) error {
	return deleteAppPreset(name)
}

func (s *Service) ListDomainLists(_ struct{}, reply *[]string) error {
	names, err := listDomainListNames()
	if err != nil {
		return err
	}
	*reply = names
	return nil
}

func (s *Service) SaveDomainList(req DomainListSaveRequest, _ *struct{}) error {
	if err := saveDomainList(req.Name, req.Domains); err != nil {
		return err
	}
	// Live-update every connected profile that has this list enabled —
	// otherwise editing a list's own domains while it's already toggled
	// on for a connected profile wouldn't take effect until a reconnect.
	s.mu.Lock()
	bridge := s.bridge
	s.mu.Unlock()
	if bridge == nil {
		return nil
	}
	for _, name := range bridge.TunnelNames() {
		cfg, err := loadProfile(name)
		if err != nil {
			continue
		}
		for _, enabled := range cfg.Interface.IncludedDomainLists {
			if enabled == req.Name {
				bridge.UpdateDomains(name, resolveDomainLists(cfg.Interface.IncludedDomainLists))
				break
			}
		}
	}
	return nil
}

func (s *Service) LoadDomainList(name string, reply *[]string) error {
	domains, err := loadDomainList(name)
	if err != nil {
		return err
	}
	*reply = domains
	return nil
}

func (s *Service) DeleteDomainList(name string, _ *struct{}) error {
	return deleteDomainList(name)
}

// winService adapts Service to svc.Handler for svc.Run.
type winService struct {
	svc *Service
}

func (w *winService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	changes <- svc.Status{State: svc.StartPending}

	listener, err := ipcListen()
	if err != nil {
		log.Printf("ipcListen: %v", err)
		return false, 1
	}

	// Safety net for Bridge.Start's IPv6 firewall block (bridge_windows.go):
	// a clean Disconnect/service-stop removes the rule via Stop(), but an
	// unclean kill (crash, `Stop-Service -Force`, power loss) skips that
	// and would otherwise leave outbound IPv6 blocked system-wide even
	// while disconnected. It runs PowerShell, so it happens in the
	// background AFTER reporting Running — it used to run before svc.Run
	// even reached the SCM, and on a slow or freshly booted machine
	// (PowerShell cold start, antivirus scanning) it blew through the
	// SCM's 30s start timeout: the service failed to start at all (event
	// 7009) and nothing could connect. Reproduced 2026-09-29 on a Windows
	// 11 VM. Connect waits for it (see ipv6CleanupDone) so it can never
	// remove the rule a fresh full-tunnel connect has just added.
	cleanupDone := make(chan struct{})
	w.svc.ipv6CleanupDone = cleanupDone
	go func() {
		defer close(cleanupDone)
		if err := removeIPv6FirewallRule(); err != nil {
			log.Printf("startup IPv6 safety net: %v", err)
		}
	}()

	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- serveIPC(listener, w.svc) }()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Printf("%s running, control pipe %s", serviceName, pipePath)
	go tryRecoverPendingUpdate()
	go w.svc.autoConnectOnStartup()
	// Background update check: runs once at startup (after a short delay so
	// the service settles) and then every hour, independently of whether the
	// GUI is open. This means updates install on machines that run headlessly
	// or whose users never open the GUI themselves.
	go func() {
		time.Sleep(2 * time.Minute)
		for {
			w.svc.CheckAndInstallUpdate(struct{}{}, new(struct{})) //nolint:errcheck
			time.Sleep(updateCheckThrottle)
		}
	}()

loop:
	for {
		select {
		case err := <-serveErrCh:
			log.Printf("serveIPC exited: %v", err)
			break loop
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				break loop
			}
		}
	}

	changes <- svc.Status{State: svc.StopPending}
	listener.Close()
	w.svc.mu.Lock()
	w.svc.disconnectLocked()
	w.svc.mu.Unlock()
	return false, 0
}

func runService() error {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	// The service has no console at all (started by the SCM), so the
	// default log.SetOutput(os.Stderr) goes nowhere anyone could read it —
	// every Bridge routing decision (handleForwarded/handleUDPForwarded's
	// "-> TUNNEL"/"-> direct" logging) was silently discarded. Same
	// ProgramData directory as the profile store (profiles.go), so it
	// lives alongside the data it's describing.
	if dir, err := profilesDirectory(); err == nil {
		logPath := filepath.Join(filepath.Dir(dir), "service.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			log.SetOutput(f)
		} else {
			log.Printf("could not open service log file %s: %v", logPath, err)
		}
	}

	return svc.Run(serviceName, &winService{svc: &Service{}})
}

// waitForServiceStop signals svc to stop and blocks until its process has
// actually exited (State == svc.Stopped), or timeout elapses. svc.Control
// only SIGNALS a stop request — it returns as soon as the request is
// queued, not once the process has exited, which used to race the
// installer's own file-replacement step: RemoveFiles could run while the
// still-shutting-down spectrune.exe/wintun.dll (bridge.go's Stop() tears
// down the Wintun adapter and removes the IPv6 firewall rule, which takes
// a moment) still held its own image file locked, tripping Windows
// Installer's FilesInUse/Restart-Manager fallback ("close and restart to
// continue") on what should have been a plain upgrade. Best-effort: a
// service that's already stopped, or that doesn't respond to Stop at all,
// isn't treated as fatal here — the caller's own Delete/CreateService
// call will surface any real problem.
func waitForServiceStop(s *mgr.Service, timeout time.Duration) {
	s.Control(svc.Stop)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil || status.State == svc.Stopped {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// installService registers this exe (invoked with the hidden "/service"
// arg, matching how AmneziaWGManager's own service registration works —
// see manager/install.go's InstallTunnel) as a LocalSystem auto-start
// service, then starts it.
func installService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("mgr.Connect: %w", err)
	}
	defer m.Disconnect()

	path, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	if existing, err := m.OpenService(serviceName); err == nil {
		waitForServiceStop(existing, 15*time.Second)
		existing.Delete()
		existing.Close()
	}

	config := mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  "Spectrune",
		Description:  "Per-application VPN routing (Spectrune)",
	}
	service, err := m.CreateService(serviceName, path, config, "/service")
	if err != nil {
		return fmt.Errorf("CreateService: %w", err)
	}
	defer service.Close()

	// Recovery actions — SCM otherwise leaves a freshly-created service
	// with none at all, meaning the whole VPN goes silently dark until
	// someone notices and manually starts it again. Confirmed live
	// 2026-09-21: an operator's own `Stop-Process -Name spectrune -Force`
	// meant to close a couple of stray /gui windows matched the /service
	// process too (Get-Process/Stop-Process key on the image name, which
	// is identical for /gui and /service — the command line arguments
	// that actually distinguish them aren't visible to a plain name
	// match), silently killing the real service; nothing brought it back
	// until that was noticed from the resulting "no VPN" report. Restart
	// after 5s/10s/30s (first/second/subsequent failures), reset the
	// failure count after a day of stability so a bad patch doesn't
	// permanently exhaust a "3 tries" budget. Covers a real crash and,
	// via SetRecoveryActionsOnNonCrashFailures, a clean-but-wrong exit
	// too — not just the exact accidental-kill scenario above, which
	// already reports as a crash (WIN32_EXIT_CODE 1067) and would have
	// been covered by SetRecoveryActions alone.
	recoveryActions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := service.SetRecoveryActions(recoveryActions, uint32((24 * time.Hour).Seconds())); err != nil {
		log.Printf("installService: SetRecoveryActions failed (non-fatal): %v", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		log.Printf("installService: SetRecoveryActionsOnNonCrashFailures failed (non-fatal): %v", err)
	}

	if err := service.Start(); err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("mgr.Connect: %w", err)
	}
	defer m.Disconnect()

	service, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("OpenService: %w", err)
	}
	defer service.Close()

	waitForServiceStop(service, 15*time.Second)
	return service.Delete()
}
