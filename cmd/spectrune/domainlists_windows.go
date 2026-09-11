// Named, reusable domain lists — e.g. a list called "discord" holding
// every Discord domain. A profile enables zero or more lists
// (conf.Interface.IncludedDomainLists, just the names) rather than
// storing a flat domain set itself, so the same list can be toggled
// on/off independently across profiles and edited in one place. Same
// JSON-file-per-list shape as apppresets_windows.go, and same directory
// convention (ProgramData\Spectrune) as profiles.go/apppresets.go.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const domainListFileSuffix = ".json"

// DomainListSaveRequest bundles Service.SaveDomainList's two arguments
// into one — net/rpc methods take exactly one request value.
type DomainListSaveRequest struct {
	Name    string
	Domains []string
}

var cachedDomainListsDir string

func domainListsDirectory() (string, error) {
	if cachedDomainListsDir != "" {
		return cachedDomainListsDir, nil
	}
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("KnownFolderPath(ProgramData): %w", err)
	}
	dir := filepath.Join(root, "Spectrune", "DomainLists")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("MkdirAll(%s): %w", dir, err)
	}
	cachedDomainListsDir = dir
	return dir, nil
}

func listDomainListNames() ([]string, error) {
	dir, err := domainListsDirectory()
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
		if !strings.HasSuffix(name, domainListFileSuffix) || !e.Type().IsRegular() {
			continue
		}
		name = strings.TrimSuffix(name, domainListFileSuffix)
		if !profileNameIsValid(name) {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func domainListPath(name string) (string, error) {
	if !profileNameIsValid(name) {
		return "", fmt.Errorf("domain list name %q is not valid", name)
	}
	dir, err := domainListsDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+domainListFileSuffix), nil
}

func saveDomainList(name string, domains []string) error {
	path, err := domainListPath(name)
	if err != nil {
		return err
	}
	data, err := json.Marshal(domains)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadDomainList(name string) ([]string, error) {
	path, err := domainListPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var domains []string
	if err := json.Unmarshal(data, &domains); err != nil {
		return nil, err
	}
	return domains, nil
}

func deleteDomainList(name string) error {
	path, err := domainListPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// resolveDomainLists turns a profile's IncludedDomainLists (list names)
// into the flat, deduplicated domain set the outbound tunnel's DNS
// sniffer actually watches — mirrors domainlists_linux.go's function of
// the same name.
func resolveDomainLists(listNames []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, name := range listNames {
		domains, err := loadDomainList(name)
		if err != nil {
			continue
		}
		for _, d := range domains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}
