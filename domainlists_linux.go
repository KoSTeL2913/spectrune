// Named, reusable domain lists — e.g. a list called "дс" holding every
// Discord domain, "тг" holding Telegram's. A profile enables zero or
// more lists (LinuxConfig.IncludedDomainLists, just the names) rather
// than storing a flat domain set itself, so the same list can be
// toggled on/off independently across profiles and edited in one place.
// Same JSON-file-per-list shape as apppresets_linux.go.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const domainListsDir = "/var/lib/spectrune/domainlists"

func domainListPath(name string) string {
	return filepath.Join(domainListsDir, name+".json")
}

func listDomainListNames() ([]string, error) {
	if err := os.MkdirAll(domainListsDir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(domainListsDir)
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

func saveDomainList(name string, domains []string) error {
	if !profileNameIsValid(name) {
		return fmt.Errorf("domain list name %q is not valid", name)
	}
	if err := os.MkdirAll(domainListsDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(domains)
	if err != nil {
		return err
	}
	return os.WriteFile(domainListPath(name), data, 0o600)
}

func loadDomainList(name string) ([]string, error) {
	data, err := os.ReadFile(domainListPath(name))
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
	return os.Remove(domainListPath(name))
}

// DomainListSaveRequest bundles SaveDomainList's two arguments — net/rpc
// methods take exactly one request value.
type DomainListSaveRequest struct {
	Name    string
	Domains []string
}

// resolveDomainLists turns a profile's IncludedDomainLists (list names)
// into the flat, deduplicated domain set domainSniffer actually watches.
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
