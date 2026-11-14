# Repeated GUI launch regression tests

Linux (requires a built binary, GTK/WebKit, a D-Bus session, Xvfb, Metacity,
xdotool, wmctrl and Python 3):

```
dbus-run-session -- xvfb-run -a python3 tests/gui_repeat_linux.py /path/to/spectrune
```

The optional `--baseline` argument reproduces the missing single-instance guard
in releases before this fix. Each run uses an isolated display and configuration.

Windows 10, in the logged-in interactive user's desktop:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File tests/gui_repeat_windows.ps1 -Binary 'C:\Program Files\Spectrune\spectrune.exe'
```

Both tests verify that the original PID/window survives repeated launches,
bursts of launches, minimizing and hiding to the tray. The Windows test also
checks `/gui-restart`. If the Windows test creates its own primary GUI, it tests
a simultaneous first-launch race and removes its own processes afterwards;
it leaves a pre-existing user GUI running.

Windows mutex ownership, retry and error handling are additionally covered by
`gui_instance_windows_test.go` using real named kernel objects.
