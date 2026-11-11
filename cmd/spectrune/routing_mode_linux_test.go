package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveRoutingModeChanges(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands")
	t.Setenv("ROUTING_TEST_LOG", logPath)
	t.Setenv("PATH", dir)
	for _, name := range []string{"ip", "resolvectl", "sysctl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ROUTING_TEST_LOG\"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	b := &LinuxBridge{}
	for _, tt := range []struct {
		name        string
		apps, lists []string
		full        bool
	}{
		{"start full", nil, nil, true},
		{"switch to domain only", nil, []string{"missing-list"}, false},
		{"stay restricted with empty list", nil, []string{"another-missing-list"}, false},
		{"add app", []string{"missing-app.desktop"}, []string{"missing-list"}, false},
		{"remove domain list with app retained", []string{"missing-app.desktop"}, nil, false},
		{"remove all selections", nil, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0600); err != nil {
				t.Fatal(err)
			}
			b.UpdateSelection(tt.apps, tt.lists)
			if b.full != tt.full {
				t.Fatalf("full = %v, want %v", b.full, tt.full)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.full && strings.Contains(string(log), "rule add not fwmark") {
				t.Fatalf("restricted selection installed catch-all: %s", log)
			}
			if tt.name == "switch to domain only" && !strings.Contains(string(log), "rule del priority 31001") {
				t.Fatalf("full-tunnel rule was not removed: %s", log)
			}
		})
	}
}
