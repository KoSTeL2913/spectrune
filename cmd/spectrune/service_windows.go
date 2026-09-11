// The Spectrune Windows service: owns the one active *Bridge (if any)
// and exposes it over IPC (see ipc.go) as Connect/Disconnect/State/
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
	"os/exec"
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
	mu            sync.Mutex
	bridge        *Bridge
	activeProfile string
}

type StateReply struct {
	Connected   bool
	ProfileName string
	// HandshakeOK is false while Connected is true but the WireGuard
	// handshake with the peer hasn't completed yet (or ever will, if the
	// config/server is bad) — see Bridge.HandshakeOK. Connected alone
	// only ever meant "the local adapter came up," which used to read as
	// a false "it's working" in the UI even when the tunnel was
	// completely dead.
	HandshakeOK bool
}

func (s *Service) Connect(name string, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridge != nil {
		return fmt.Errorf("already connected (profile %q) — disconnect first", s.activeProfile)
	}
	cfg, err := loadProfile(name)
	if err != nil {
		return fmt.Errorf("loadProfile(%q): %w", name, err)
	}
	b := &Bridge{}
	if err := b.Start(cfg); err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	s.bridge = b
	s.activeProfile = name
	return nil
}

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
	s.activeProfile = ""
	return nil
}

func (s *Service) State(_ struct{}, reply *StateReply) error {
	s.mu.Lock()
	reply.Connected = s.bridge != nil
	reply.ProfileName = s.activeProfile
	bridge := s.bridge
	s.mu.Unlock()
	// HandshakeOK's IpcGet() call runs outside the lock — State is polled
	// every 2-3s by both the tray and the GUI, and holding s.mu (the same
	// lock Connect/Disconnect/SaveProfile/DeleteProfile all need) for the
	// duration of a device query neither needs nor benefits from that
	// lock would let frequent polling stall those instead of just this
	// one read.
	if bridge != nil {
		reply.HandshakeOK = bridge.HandshakeOK()
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

func (s *Service) ListProfiles(_ struct{}, reply *[]string) error {
	names, err := listProfileNames()
	if err != nil {
		return err
	}
	*reply = names
	return nil
}

func (s *Service) SaveProfile(cfg conf.Config, _ *struct{}) error {
	if err := saveProfile(&cfg); err != nil {
		return err
	}

	// If this is the profile that's actually connected right now, push
	// the new app selection into the running tunnel immediately instead
	// of leaving it stale until the next Disconnect/Connect — that used
	// to mean tearing down and rebuilding the whole adapter/tunnel (and
	// dropping every other in-flight connection with it) just to change
	// which apps route through it.
	s.mu.Lock()
	bridge := s.bridge
	isActive := s.bridge != nil && s.activeProfile == cfg.Name
	s.mu.Unlock()
	if isActive {
		bridge.UpdateIncludedApps(cfg.Interface.IncludedApps)
		bridge.UpdateDomains(resolveDomainLists(cfg.Interface.IncludedDomainLists))
	}

	// At most one profile can be marked AutoConnect — the whole app only
	// ever runs one tunnel at a time, so "auto-connect on startup" only
	// makes sense as a single choice. Enforced here rather than trusting
	// the GUI to do it, so it holds regardless of how a profile gets
	// saved (CLI, a future second client, ...).
	if cfg.Interface.AutoConnect {
		if err := clearAutoConnectExcept(cfg.Name); err != nil {
			log.Printf("SaveProfile: clearAutoConnectExcept(%q): %v", cfg.Name, err)
		}
	}
	return nil
}

// clearAutoConnectExcept unsets AutoConnect on every saved profile other
// than keep — see SaveProfile's doc for why only one may have it set.
func clearAutoConnectExcept(keep string) error {
	names, err := listProfileNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == keep {
			continue
		}
		cfg, err := loadProfile(name)
		if err != nil {
			log.Printf("clearAutoConnectExcept: loadProfile(%q): %v", name, err)
			continue
		}
		if !cfg.Interface.AutoConnect {
			continue
		}
		cfg.Interface.AutoConnect = false
		if err := saveProfile(cfg); err != nil {
			log.Printf("clearAutoConnectExcept: saveProfile(%q): %v", name, err)
		}
	}
	return nil
}

// autoConnectOnStartup looks for exactly one saved profile with
// AutoConnect enabled and connects to it — mirrors clicking Connect in
// the GUI, just triggered once at service start instead of by the user.
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
		return
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
	if s.activeProfile == name {
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
// the active connection (activeProfile tracking, service.log's own
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
	if s.activeProfile == req.OldName {
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
	// Live-update any connected profile that has this list enabled —
	// otherwise editing a list's own domains while it's already toggled
	// on for the active profile wouldn't take effect until a reconnect.
	s.mu.Lock()
	bridge := s.bridge
	name := s.activeProfile
	s.mu.Unlock()
	if bridge == nil {
		return nil
	}
	cfg, err := loadProfile(name)
	if err != nil {
		return nil
	}
	for _, enabled := range cfg.Interface.IncludedDomainLists {
		if enabled == req.Name {
			bridge.UpdateDomains(resolveDomainLists(cfg.Interface.IncludedDomainLists))
			break
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

	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- serveIPC(listener, w.svc) }()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Printf("%s running, control pipe %s", serviceName, pipePath)
	go w.svc.autoConnectOnStartup()

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

	// Safety net for Bridge.Start's IPv6 firewall block (bridge.go): a
	// clean Disconnect/service-stop removes the rule via Stop(), but an
	// unclean kill (crash, `Stop-Service -Force`, power loss) skips that
	// and would otherwise leave outbound IPv6 blocked system-wide even
	// while disconnected. Unconditionally removing it on every service
	// start is a no-op when it was never added, so it's cheap insurance.
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Remove-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue", ipv6FirewallRuleName)).CombinedOutput(); err != nil {
		log.Printf("startup IPv6 safety net: Remove-NetFirewallRule failed: %v (%s)", err, string(out))
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
