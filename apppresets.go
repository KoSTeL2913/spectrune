// App presets: named, reusable lists of application paths for the Apps
// picker (webui_html.go's view-apps) — saved once ("Firefox + Discord +
// Steam"), then loaded straight into any profile's selection instead of
// re-searching and re-checking the same apps every time. Deliberately not
// DPAPI-encrypted like profiles.go's tunnel configs: a preset is just a
// list of exe paths, nothing sensitive, so a plain JSON file keeps this
// simple. Name validation reuses profiles.go's profileNameIsValid — it's
// really just "safe as a filename", nothing tunnel-specific about it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const appPresetFileSuffix = ".json"

// AppPresetSaveRequest bundles Service.SaveAppPreset's two arguments into
// one — net/rpc methods take exactly one request value.
type AppPresetSaveRequest struct {
	Name string
	Apps []string
}

var cachedAppPresetsDir string

// appPresetsDirectory returns (creating if needed) the app-preset store,
// alongside profiles.go's own ProgramData\Spectrune tree.
func appPresetsDirectory() (string, error) {
	if cachedAppPresetsDir != "" {
		return cachedAppPresetsDir, nil
	}
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("KnownFolderPath(ProgramData): %w", err)
	}
	dir := filepath.Join(root, "Spectrune", "AppPresets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("MkdirAll(%s): %w", dir, err)
	}
	cachedAppPresetsDir = dir
	return dir, nil
}

func listAppPresetNames() ([]string, error) {
	dir, err := appPresetsDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, appPresetFileSuffix) || !e.Type().IsRegular() {
			continue
		}
		name = strings.TrimSuffix(name, appPresetFileSuffix)
		if !profileNameIsValid(name) {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func appPresetPath(name string) (string, error) {
	if !profileNameIsValid(name) {
		return "", fmt.Errorf("preset name %q is not valid", name)
	}
	dir, err := appPresetsDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+appPresetFileSuffix), nil
}

func saveAppPreset(name string, apps []string) error {
	path, err := appPresetPath(name)
	if err != nil {
		return err
	}
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadAppPreset(name string) ([]string, error) {
	path, err := appPresetPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
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
	path, err := appPresetPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
