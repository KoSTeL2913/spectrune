// Profile storage for Spectrune's own tunnel configs — deliberately NOT
// conf.Save/LoadFromName/ListConfigNames/DeleteName from the amneziawg-
// windows conf package: those are hardcoded to
// %ProgramFiles%\AmneziaWG\Data\Configurations (see conf/path_windows.go's
// RootDirectory), i.e. the *other* app's own tunnel store. Reusing that
// path would mix Spectrune's profiles into AmneziaWG's own tunnel list
// and risk name collisions between two unrelated apps. Everything else —
// wg-quick parsing/serialization (conf.FromWgQuick/ToWgQuick), the DPAPI
// primitives (conf/dpapi), name validation (conf.TunnelNameIsValid) — is
// still reused as-is; only the storage location and the directory/file
// listing glue are our own.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amnezia-vpn/amneziawg-windows/v3/conf"
	"github.com/amnezia-vpn/amneziawg-windows/v3/conf/dpapi"
	"golang.org/x/sys/windows"
)

const profileFileSuffix = ".conf.dpapi"

// placeholderTunnelName is fed to conf.FromWgQuick in place of the real
// profile name — see profileNameIsValid's doc for why the real name can't
// go through conf's own TunnelNameIsValid check. Any name accepted by
// conf.TunnelNameIsValid works here; this one just needs to exist.
const placeholderTunnelName = "spectrune-profile"

var cachedProfilesDir string

var profileForbiddenChars = "/\\:*?\"<>|\x00"

var profileReservedNames = []string{
	"CON", "PRN", "AUX", "NUL",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
}

// profileNameIsValid checks that name is safe as this store's filename.
// Deliberately NOT conf.TunnelNameIsValid, which restricts names to
// ASCII "^[a-zA-Z0-9_=+.-]{1,32}$" — a rule that makes sense for the
// *official* AmneziaWG client, where a tunnel's name doubles as its
// Windows service/adapter name, but doesn't apply here: Spectrune's
// Windows adapter and service both use the fixed constant "Spectrune"
// (see adapterName/serviceName) regardless of profile name, so a profile
// name is just a DPAPI blob's filename. This only rules out what Windows
// filenames actually can't contain, letting through any language —
// Cyrillic included.
func profileNameIsValid(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name || len([]rune(name)) > 64 {
		return false
	}
	if strings.HasSuffix(name, ".") {
		return false
	}
	if strings.ContainsAny(name, profileForbiddenChars) {
		return false
	}
	for _, r := range name {
		if r < 0x20 {
			return false
		}
	}
	for _, reserved := range profileReservedNames {
		if strings.EqualFold(name, reserved) {
			return false
		}
	}
	return true
}

// profilesDirectory returns (creating if needed) our own profile store,
// under ProgramData rather than ProgramFiles — this is mutable app data
// owned by the service (always running as SYSTEM), not part of the
// installed program image, so ProgramData is the conventional home for it
// regardless of where spectrune.exe itself happens to be installed.
func profilesDirectory() (string, error) {
	if cachedProfilesDir != "" {
		return cachedProfilesDir, nil
	}
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("KnownFolderPath(ProgramData): %w", err)
	}
	dir := filepath.Join(root, "Spectrune", "Profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("MkdirAll(%s): %w", dir, err)
	}
	cachedProfilesDir = dir
	return dir, nil
}

// listProfileNames returns the names of all saved profiles, same filtering
// rules as conf.ListConfigNames (valid name, regular file, not
// permission-stripped) so profile names stay usable as Windows adapter/
// service-adjacent identifiers later.
func listProfileNames() ([]string, error) {
	dir, err := profilesDirectory()
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
		if !strings.HasSuffix(name, profileFileSuffix) || !e.Type().IsRegular() {
			continue
		}
		name = strings.TrimSuffix(name, profileFileSuffix)
		if !profileNameIsValid(name) {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func profilePath(name string) (string, error) {
	if !profileNameIsValid(name) {
		return "", fmt.Errorf("profile name %q is not valid", name)
	}
	dir, err := profilesDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+profileFileSuffix), nil
}

// saveProfile DPAPI-encrypts cfg (keyed by its own name, same convention
// conf.Config.Save uses) and writes it to our own store, overwriting any
// existing profile of the same name.
func saveProfile(cfg *conf.Config) error {
	path, err := profilePath(cfg.Name)
	if err != nil {
		return err
	}
	plaintext := []byte(cfg.ToWgQuick())
	ciphertext, err := dpapi.Encrypt(plaintext, cfg.Name)
	if err != nil {
		return fmt.Errorf("dpapi.Encrypt: %w", err)
	}
	return os.WriteFile(path, ciphertext, 0o600)
}

func loadProfile(name string) (*conf.Config, error) {
	path, err := profilePath(name)
	if err != nil {
		return nil, err
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	plaintext, err := dpapi.Decrypt(ciphertext, name)
	if err != nil {
		return nil, fmt.Errorf("dpapi.Decrypt: %w", err)
	}
	// FromWgQuick runs its own conf.TunnelNameIsValid check (ASCII-only) —
	// irrelevant here (see profileNameIsValid's doc), so it gets a name
	// that's guaranteed to pass, and the real one is restored right after.
	cfg, err := conf.FromWgQuick(string(plaintext), placeholderTunnelName)
	if err != nil {
		return nil, err
	}
	cfg.Name = name
	return cfg, nil
}

func deleteProfile(name string) error {
	path, err := profilePath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
