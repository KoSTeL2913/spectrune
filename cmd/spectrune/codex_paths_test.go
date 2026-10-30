package main

import "testing"

func TestCodexAppKey(t *testing.T) {
	daemon := `C:\Users\alice\.codex\packages\app-server-daemon\releases\0.166.0-x86_64-pc-windows-msvc\bin\codex.exe`
	cli := `C:\Users\alice\AppData\Roaming\npm\node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe`
	want := codexAppKey(daemon)
	if want == "" || codexAppKey(cli) != want {
		t.Fatal("CLI and daemon must share a per-user routing key")
	}
	for _, path := range []string{
		`C:\Users\alice\.codex\packages\app-server-daemon\releases\0.200.0-x86_64-pc-windows-msvc\bin\codex.exe`,
		`c:/users/alice/.codex/packages/app-server-daemon/releases/next/bin/codex.exe`,
	} {
		if codexAppKey(path) != want {
			t.Errorf("updated path does not match: %s", path)
		}
	}
	for _, path := range []string{
		`C:\Users\bob\.codex\packages\app-server-daemon\releases\v1\bin\codex.exe`,
		`C:\tools\codex.exe`,
		`C:\Users\alice\.codex\packages\other\releases\v1\bin\codex.exe`,
	} {
		if codexAppKey(path) == want {
			t.Errorf("unrelated executable matched: %s", path)
		}
	}
}
