package main

import (
	"golang.org/x/net/dns/dnsmessage"
	"testing"
	"time"
)

func TestDNSCacheSupportsSelectionAfterResolution(t *testing.T) {
	name, _ := dnsmessage.NewName("example.test.")
	msg := dnsmessage.Message{Header: dnsmessage.Header{Response: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 1}}}}}
	var cache domainAnswerCache
	payload, _ := msg.Pack()
	cache.observe(payload)
	if !selectedDomainIPs(cache.snapshot(), []string{"example.test"})["203.0.113.1"] {
		t.Fatal("previously unselected name was not cached")
	}
	msg.Questions[0].Type = dnsmessage.TypeAAAA
	msg.Answers = nil
	payload, _ = msg.Pack()
	cache.observe(payload)
	if len(cache.snapshot()) != 1 {
		t.Fatal("AAAA query discarded the IPv4 cache")
	}
	cache.mu.Lock()
	a := cache.answers["example.test"]
	a.expires = time.Now().Add(-time.Second)
	cache.answers["example.test"] = a
	cache.mu.Unlock()
	if len(cache.snapshot()) != 0 {
		t.Fatal("expired answer retained")
	}
}
