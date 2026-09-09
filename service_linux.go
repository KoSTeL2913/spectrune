// The Spectrune Linux daemon: owns the one active *LinuxBridge (if any)
// and exposes it over IPC (ipc_linux.go) — same RPC contract shape as
// service_windows.go's Service (Connect/Disconnect/State/ListProfiles/
// SaveProfile/...), plus ListApps/LaunchApp which have no Windows
// equivalent (see apps_linux.go). Runs as a plain foreground process under
// systemd (Type=simple), not a Windows-style SCM service — Linux doesn't
// need that abstraction, systemd already handles start/stop/restart.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Service is the RPC-exposed surface, registered under the name "Bridge"
// (see ipc_linux.go's serveIPC) so calls are "Bridge.Connect" etc., same
// as the Windows daemon.
type Service struct {
	mu            sync.Mutex
	bridge        *LinuxBridge
	activeProfile string
}

type StateReply struct {
	Connected   bool
	ProfileName string
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
	b := &LinuxBridge{}
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
	if bridge != nil {
		reply.HandshakeOK = bridge.HandshakeOK()
	}
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

func (s *Service) SaveProfile(cfg LinuxConfig, _ *struct{}) error {
	if err := saveProfile(&cfg); err != nil {
		return err
	}
	// If this is the profile that's actually connected right now, push the
	// new app selection into the running routing setup immediately —
	// mirrors service_windows.go's SaveProfile, and matters more here than
	// it used to: editing the Apps list is now how you tell an
	// already-running app "start tunneling," not just a pre-connect setup
	// step.
	s.mu.Lock()
	bridge := s.bridge
	isActive := s.bridge != nil && s.activeProfile == cfg.Name
	s.mu.Unlock()
	if isActive {
		bridge.UpdateIncludedApps(cfg.IncludedApps)
	}
	return nil
}

func (s *Service) LoadProfile(name string, reply *LinuxConfig) error {
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

type RenameProfileRequest struct {
	OldName string
	NewName string
}

func (s *Service) RenameProfile(req RenameProfileRequest, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile == req.OldName {
		return fmt.Errorf("profile %q is currently connected — disconnect first", req.OldName)
	}
	if req.OldName == req.NewName {
		return nil
	}
	return renameProfile(req.OldName, req.NewName)
}

func (s *Service) ListApps(_ struct{}, reply *[]DesktopApp) error {
	apps, err := listDesktopApps()
	if err != nil {
		return err
	}
	*reply = apps
	return nil
}

// AppIconValue returns a .desktop entry's raw Icon= value (either a theme
// icon name or an absolute path — resolving either into an actual image
// happens GUI-side, in icons_linux.go, since that's where GTK's icon
// theme (a desktop-session, not-root concern) is already linked in).
func (s *Service) AppIconValue(desktopID string, reply *string) error {
	apps, err := listDesktopApps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID == desktopID {
			*reply = a.Icon
			return nil
		}
	}
	return fmt.Errorf("no such app %q", desktopID)
}

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

func (s *Service) LaunchApp(req LaunchAppRequest, _ *struct{}) error {
	return launchApp(req)
}

// autoConnectOnStartup mirrors service_windows.go's — looks for exactly
// one saved profile with AutoConnect set and connects to it once at
// daemon startup.
func (s *Service) autoConnectOnStartup() {
	names, err := listProfileNames()
	if err != nil {
		log.Printf("autoConnectOnStartup: listProfileNames: %v", err)
		return
	}
	for _, name := range names {
		cfg, err := loadProfile(name)
		if err != nil {
			continue
		}
		if !cfg.AutoConnect {
			continue
		}
		log.Printf("autoConnectOnStartup: connecting to %q", name)
		if err := s.Connect(name, &struct{}{}); err != nil {
			log.Printf("autoConnectOnStartup: Connect(%q): %v", name, err)
		}
		return
	}
}

// runDaemon is the /service entry point — a plain foreground process,
// meant to be run under systemd (Type=simple), which handles
// start/stop/restart/autostart itself; no SCM-style API needed like
// Windows' svc.Run.
func runDaemon() error {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if os.Geteuid() != 0 {
		return fmt.Errorf("spectruned must run as root (needs CAP_NET_ADMIN for namespaces/TUN)")
	}

	listener, err := ipcListen()
	if err != nil {
		return fmt.Errorf("ipcListen: %w", err)
	}
	defer listener.Close()

	svc := &Service{}
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- serveIPC(listener, svc) }()

	log.Printf("spectruned running, control socket %s", socketPath)
	go svc.autoConnectOnStartup()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-serveErrCh:
		log.Printf("serveIPC exited: %v", err)
	case sig := <-sigCh:
		log.Printf("received %v, shutting down", sig)
	}

	svc.mu.Lock()
	svc.disconnectLocked()
	svc.mu.Unlock()
	return nil
}
