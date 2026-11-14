package main

import "testing"

func TestNormalizeDomainRule(t *testing.T) {
	cases := map[string]string{
		"chatgpt.com":       "chatgpt.com",
		" CHATGPT.COM. ":    "chatgpt.com",
		"*.openai.com":      "openai.com",
		"*.API.OpenAI.COM.": "api.openai.com",
	}
	for input, want := range cases {
		if got := normalizeDomainRule(input); got != want {
			t.Errorf("normalizeDomainRule(%q) = %q; want %q", input, got, want)
		}
	}
}

func TestDomainBoundaryMatching(t *testing.T) {
	rule := normalizeDomainRule("*.openai.com")
	for _, tc := range []struct {
		domain string
		want   bool
	}{
		{"openai.com", true},
		{"api.openai.com", true},
		{"a.b.openai.com", true},
		{"fakeopenai.com", false},
		{"openai.com.example.org", false},
	} {
		domain := normalizeDomainRule(tc.domain)
		got := domain == rule || (len(domain) > len(rule) && domain[len(domain)-len(rule)-1:] == "."+rule)
		if got != tc.want {
			t.Errorf("match(%q, %q) = %v; want %v", domain, rule, got, tc.want)
		}
	}
}
