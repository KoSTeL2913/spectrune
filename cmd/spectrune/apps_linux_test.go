package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCustomExecutableSelection(t *testing.T) {
	p := filepath.Join(t.TempDir(), "custom-app")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	got := resolveAppBinaries([]string{p})
	if len(got) != 1 || got[0] != "custom-app" {
		t.Fatalf("custom executable not selected: %v", got)
	}
}
func TestEnvironmentWrappedDesktopCommand(t *testing.T) {
	for _, cmd := range []string{"env SOME_VAR=value /opt/example/bin/example %U", "env -- SOME_VAR=value /opt/example/bin/example %U"} {
		if got := execTargetBase(cmd); got != "example" {
			t.Fatalf("%s resolves to %q", cmd, got)
		}
	}
}
