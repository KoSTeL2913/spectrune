package main

import "strings"

// Keep DNS answers associated with the name that produced them. A flat IP
// set cannot remove a disabled domain without also losing retained domains.
func selectedDomainIPs(answers map[string][]string, domains []string) map[string]bool {
	ips := make(map[string]bool)
	for name, addresses := range answers {
		if matchingSelectedDomain(name, domains) == "" {
			continue
		}
		for _, ip := range addresses {
			ips[ip] = true
		}
	}
	return ips
}

func matchingSelectedDomain(name string, domains []string) string {
	name = normalizeDomainRule(name)
	for _, domain := range domains {
		if name == domain || strings.HasSuffix(name, "."+domain) {
			return domain
		}
	}
	return ""
}
