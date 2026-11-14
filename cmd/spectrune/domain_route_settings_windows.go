package main

import "path/filepath"

func domainRouteSettingsPath() (string, error) {
	dir, err := profilesDirectory()
	return filepath.Join(dir, "domain-routes.json"), err
}
func (s *Service) refreshDomainRoutesLocked() error {
	if s.bridge == nil {
		return nil
	}
	b := s.bridge
	// Retain observed answers across a live reassignment: clients may still
	// have these records cached and need not issue another DNS query.
	b.tunMu.Lock()
	answers := b.dnsCache.snapshot()
	for _, nt := range b.tunnels {
		nt.t.mu.Lock()
		for name, ips := range nt.t.domainAnswers {
			if _, ok := answers[name]; !ok {
				answers[name] = append([]string(nil), ips...)
			}
		}
		nt.t.mu.Unlock()
	}
	for _, nt := range b.tunnels {
		cfg, err := loadProfile(nt.name)
		if err != nil {
			b.tunMu.Unlock()
			return err
		}
		domains := resolveDomainLists(effectiveDomainLists(nt.name, cfg.Interface.IncludedDomainLists))
		nt.t.mu.Lock()
		nt.t.domains = domains
		nt.t.priorityDomains = resolveDomainLists(explicitDomainLists(nt.name))
		nt.t.domainAnswers = make(map[string][]string)
		for name, ips := range answers {
			if matchingSelectedDomain(name, domains) != "" {
				nt.t.domainAnswers[name] = ips
			}
		}
		nt.t.domainIPs = selectedDomainIPs(nt.t.domainAnswers, domains)
		nt.t.priorityIPs = selectedDomainIPs(nt.t.domainAnswers, nt.t.priorityDomains)
		nt.t.mu.Unlock()
	}
	b.tunMu.Unlock()
	b.closeStaleConns()
	return nil
}
