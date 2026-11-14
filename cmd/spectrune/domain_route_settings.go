package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// An explicit owner overrides per-profile list membership without changing
// either profile's full/app routing mode.
type DomainRouteRequest struct{ List, Profile string }

var domainRouteMu sync.Mutex

func readDomainRoutes() (map[string]string, error) {
	path, err := domainRouteSettingsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var routes map[string]string
	if err = json.Unmarshal(data, &routes); err != nil {
		return nil, err
	}
	if routes == nil {
		routes = map[string]string{}
	}
	return routes, nil
}
func effectiveDomainListsFor(profile string, included []string, routes map[string]string) []string {
	seen := map[string]bool{}
	for _, name := range included {
		if owner := routes[name]; owner == "" || owner == profile {
			seen[name] = true
		}
	}
	for name, owner := range routes {
		if owner == profile {
			seen[name] = true
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
func effectiveDomainLists(profile string, included []string) []string {
	domainRouteMu.Lock()
	defer domainRouteMu.Unlock()
	routes, err := readDomainRoutes()
	if err != nil {
		return nil
	} // do not route through a guessed owner
	return effectiveDomainListsFor(profile, included, routes)
}
func explicitDomainLists(profile string) []string {
	domainRouteMu.Lock()
	defer domainRouteMu.Unlock()
	routes, err := readDomainRoutes()
	if err != nil {
		return nil
	}
	return effectiveDomainListsFor(profile, nil, routes)
}
func (s *Service) GetDomainRoutes(_ struct{}, reply *map[string]string) error {
	domainRouteMu.Lock()
	defer domainRouteMu.Unlock()
	routes, err := readDomainRoutes()
	if err == nil {
		*reply = routes
	}
	return err
}
func (s *Service) SetDomainRoute(req DomainRouteRequest, _ *struct{}) error {
	if !profileNameIsValid(req.List) {
		return fmt.Errorf("invalid domain list name")
	}
	if _, err := loadDomainList(req.List); err != nil {
		return err
	}
	if req.Profile != "" {
		if _, err := loadProfile(req.Profile); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	domainRouteMu.Lock()
	routes, err := readDomainRoutes()
	if err == nil {
		if req.Profile == "" {
			delete(routes, req.List)
		} else {
			routes[req.List] = req.Profile
		}
		err = writeDomainRoutes(routes)
	}
	domainRouteMu.Unlock()
	if err != nil {
		return err
	}
	return s.refreshDomainRoutesLocked()
}

// Caller holds domainRouteMu. Replace the complete settings file atomically.
func writeDomainRoutes(routes map[string]string) error {
	path, err := domainRouteSettingsPath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
func changeDomainRouteReferences(list, oldProfile, newProfile string) error {
	domainRouteMu.Lock()
	defer domainRouteMu.Unlock()
	routes, err := readDomainRoutes()
	if err != nil {
		return err
	}
	changed := false
	for name, owner := range routes {
		if list != "" && name == list {
			delete(routes, name)
			changed = true
		} else if oldProfile != "" && owner == oldProfile {
			if newProfile == "" {
				delete(routes, name)
			} else {
				routes[name] = newProfile
			}
			changed = true
		}
	}
	if changed {
		return writeDomainRoutes(routes)
	}
	return nil
}
