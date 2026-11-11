package main

import "testing"

func TestFullTunnelSelection(t *testing.T) {
	for _, tt := range []struct {
		name        string
		apps, lists int
		full        bool
	}{
		{"nothing selected", 0, 0, true},
		{"domains only", 0, 1, false},
		{"apps only", 1, 0, false},
		{"apps and domains", 1, 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := fullTunnelForSelection(tt.apps, tt.lists); got != tt.full {
				t.Fatalf("full tunnel = %v, want %v", got, tt.full)
			}
		})
	}
}
