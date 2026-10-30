package main

import "strings"

// normalizeDomainRule canonicalizes DNS names and domain-list entries.
// A bare domain matches itself and all of its subdomains; wildcard syntax
// is accepted as a convenience for imported lists.
func normalizeDomainRule(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")
	domain = strings.TrimPrefix(domain, "*.")
	return domain
}
