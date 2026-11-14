package main

import "testing"

func TestDomainSelectionRemovesOnlyDisabledAnswers(t *testing.T) {
	answers := map[string][]string{"api.example.com": {"203.0.113.1", "203.0.113.3"}, "other.test": {"203.0.113.2", "203.0.113.3"}}
	ips := selectedDomainIPs(answers, []string{"example.com", "other.test"})
	if len(ips) != 3 {
		t.Fatalf("initial IPs: %v", ips)
	}
	ips = selectedDomainIPs(answers, []string{"other.test"})
	if ips["203.0.113.1"] || !ips["203.0.113.2"] || !ips["203.0.113.3"] {
		t.Fatalf("disabled domain retained or enabled answer lost: %v", ips)
	}
	if len(selectedDomainIPs(answers, nil)) != 0 {
		t.Fatal("clearing domains retained destinations")
	}
	if matchingSelectedDomain("notexample.com", []string{"example.com"}) != "" {
		t.Fatal("matched unrelated suffix")
	}
}
