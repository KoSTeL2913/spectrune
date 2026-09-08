// Profile storage for Linux — plain files under /var/lib/spectrune/profiles,
// root-owned 0600. No DPAPI equivalent needed: the trust boundary is
// already "only the root daemon reads these" via filesystem permissions,
// same guarantee DPAPI gave on Windows (SYSTEM-only), just via chmod
// instead of an encrypted blob.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const profilesDir = "/var/lib/spectrune/profiles"

var validProfileName = regexp.MustCompile(`^[A-Za-z0-9 _.-]{1,64}$`)

func profileNameIsValid(name string) bool {
	return validProfileName.MatchString(name) && !strings.ContainsAny(name, "/\\")
}

func profilePath(name string) string {
	return filepath.Join(profilesDir, name+".conf")
}

func listProfileNames() ([]string, error) {
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".conf"))
	}
	sort.Strings(names)
	return names, nil
}

func loadProfile(name string) (*LinuxConfig, error) {
	if !profileNameIsValid(name) {
		return nil, fmt.Errorf("invalid profile name %q", name)
	}
	data, err := os.ReadFile(profilePath(name))
	if err != nil {
		return nil, err
	}
	return ParseWgQuick(string(data), name)
}

func saveProfile(cfg *LinuxConfig) error {
	if !profileNameIsValid(cfg.Name) {
		return fmt.Errorf("invalid profile name %q", cfg.Name)
	}
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(profilePath(cfg.Name), []byte(cfg.ToWgQuick()), 0o600)
}

func deleteProfile(name string) error {
	if !profileNameIsValid(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	return os.Remove(profilePath(name))
}

func renameProfile(oldName, newName string) error {
	if !profileNameIsValid(newName) {
		return fmt.Errorf("profile name %q is not valid", newName)
	}
	if _, err := os.Stat(profilePath(newName)); err == nil {
		return fmt.Errorf("a profile named %q already exists", newName)
	}
	return os.Rename(profilePath(oldName), profilePath(newName))
}
