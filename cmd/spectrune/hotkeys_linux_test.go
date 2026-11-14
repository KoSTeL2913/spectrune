package main

import (
	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestHotkeyDispatchIntegration(t *testing.T) {
	if os.Getenv("SPECTRUNE_GUI_SMOKE") != "1" {
		t.Skip("requires an X display and installed daemon")
	}
	c, err := ipcDial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var state StateReply
	if err = c.Call("Bridge.State", struct{}{}, &state); err != nil {
		t.Fatal(err)
	}
	if state.Connected {
		t.Skip("leave an existing connection alone")
	}
	var cfg LinuxConfig
	source := os.Getenv("SPECTRUNE_TEST_PROFILE")
	if source == "" {
		t.Skip("set SPECTRUNE_TEST_PROFILE to a saved test configuration")
	}
	if err = c.Call("Bridge.LoadProfile", source, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Name = "audit-hotkey"
	cfg.AutoConnect = false
	cfg.Hotkey = "Ctrl+Alt+K"
	cfg.IncludedApps = []string{"audit-no-matching-app"}
	cfg.IncludedDomainLists = nil
	if err = c.Call("Bridge.SaveProfile", cfg, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	defer c.Call("Bridge.DeleteProfile", cfg.Name, &struct{}{})
	defer c.Call("Bridge.Disconnect", struct{}{}, &struct{}{})
	// A display without a window manager can leave keyboard focus at None
	// after the GUI smoke window closes; XGrabKey needs a focused descendant.
	x, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	screen := xproto.Setup(x).DefaultScreen(x)
	win, err := xproto.NewWindowId(x)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(x, screen.RootDepth, win, screen.Root, 0, 0, 100, 100, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	defer xproto.DestroyWindow(x, win)
	if err = xproto.MapWindowChecked(x, win).Check(); err != nil {
		t.Fatal(err)
	}
	if err = xproto.SetInputFocusChecked(x, xproto.InputFocusPointerRoot, win, xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
	startHotkeyManager()
	time.Sleep(time.Second)
	toggle := func(want bool) {
		t.Helper()
		if out, err := exec.Command("xdotool", "key", "ctrl+alt+k").CombinedOutput(); err != nil {
			t.Fatalf("xdotool: %v %s", err, out)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			state = StateReply{}
			if err := c.Call("Bridge.State", struct{}{}, &state); err != nil {
				t.Fatal(err)
			}
			if state.Connected == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("hotkey did not set Connected=%v", want)
	}
	toggle(true)
	if state.ProfileName != cfg.Name {
		t.Fatal("hotkey connected another profile")
	}
	toggle(false)
	t.Log("X11 hotkey connected and disconnected the test profile")
}
