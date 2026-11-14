package main

import (
	"golang.org/x/net/dns/dnsmessage"
	"net"
	"strings"
	"sync"
	"time"
)

type cachedDomainAnswer struct {
	ips     []string
	expires time.Time
}

// Keep recent DNS answers independently of the current selection, so choosing
// a route also works for names that applications have already cached.
type domainAnswerCache struct {
	mu      sync.Mutex
	answers map[string]cachedDomainAnswer
}

func (c *domainAnswerCache) observe(payload []byte) {
	var msg dnsmessage.Message
	if msg.Unpack(payload) != nil || !msg.Header.Response || msg.Header.RCode != dnsmessage.RCodeSuccess || len(msg.Questions) == 0 || msg.Questions[0].Type != dnsmessage.TypeA {
		return
	}
	var ips []string
	ttl := uint32(600)
	for _, answer := range msg.Answers {
		if a, ok := answer.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, net.IP(a.A[:]).String())
			if answer.Header.TTL < ttl {
				ttl = answer.Header.TTL
			}
		}
	}
	if len(ips) == 0 {
		return
	}
	name := strings.ToLower(strings.TrimSuffix(msg.Questions[0].Name.String(), "."))
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answers == nil {
		c.answers = make(map[string]cachedDomainAnswer)
	}
	for name, a := range c.answers {
		if !now.Before(a.expires) {
			delete(c.answers, name)
		}
	}
	if len(c.answers) >= 1024 {
		oldest := ""
		var expires time.Time
		for name, a := range c.answers {
			if oldest == "" || a.expires.Before(expires) {
				oldest, expires = name, a.expires
			}
		}
		delete(c.answers, oldest)
	}
	c.answers[name] = cachedDomainAnswer{ips: ips, expires: now.Add(time.Duration(ttl) * time.Second)}
}
func (c *domainAnswerCache) snapshot() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string][]string)
	now := time.Now()
	for name, a := range c.answers {
		if now.Before(a.expires) {
			result[name] = append([]string(nil), a.ips...)
		} else {
			delete(c.answers, name)
		}
	}
	return result
}
