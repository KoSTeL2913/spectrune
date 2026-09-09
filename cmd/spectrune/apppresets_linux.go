// Named, reusable app-selection presets for the Apps picker — the Linux
// analog of apppresets_windows.go, same JSON-file-per-preset shape, just
// under /var/lib/spectrune instead of a DPAPI-backed Windows path (same
// "root daemon only" trust boundary either way).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const appPresetsDir = "/var/lib/spectrune/apppresets"

func appPresetPath(name string) string {
	return filepath.Join(appPresetsDir, name+".json")
}

func listAppPresetNames() ([]string, error) {
	if err := os.MkdirAll(appPresetsDir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(appPresetsDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

func saveAppPreset(name string, apps []string) error {
	if !profileNameIsValid(name) {
		return fmt.Errorf("preset name %q is not valid", name)
	}
	if err := os.MkdirAll(appPresetsDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	return os.WriteFile(appPresetPath(name), data, 0o600)
}

func loadAppPreset(name string) ([]string, error) {
	data, err := os.ReadFile(appPresetPath(name))
	if err != nil {
		return nil, err
	}
	var apps []string
	if err := json.Unmarshal(data, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func deleteAppPreset(name string) error {
	return os.Remove(appPresetPath(name))
}

// AppPresetSaveRequest bundles SaveAppPreset's two arguments — net/rpc
// methods take exactly one request value, same as Windows' equivalent.
type AppPresetSaveRequest struct {
	Name string
	Apps []string
}
