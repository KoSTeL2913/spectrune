package main

import (
	"reflect"
	"testing"
)

func TestExplicitDomainOwnership(t *testing.T) {
	routes := map[string]string{"work": "third", "video": "second"}
	for _, tc := range []struct {
		profile        string
		included, want []string
	}{
		{"first", []string{"work", "other"}, []string{"other"}},
		{"second", []string{"work"}, []string{"video"}},
		{"third", nil, []string{"work"}},
	} {
		if got := effectiveDomainListsFor(tc.profile, tc.included, routes); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %v want %v", tc.profile, got, tc.want)
		}
	}
	routes["work"] = "second"
	if got := effectiveDomainListsFor("second", nil, routes); !reflect.DeepEqual(got, []string{"video", "work"}) {
		t.Fatalf("live reassignment: %v", got)
	}
	if got := effectiveDomainListsFor("third", []string{"work"}, routes); len(got) != 0 {
		t.Fatal("old owner still claims domain list")
	}
	delete(routes, "work")
	if got := effectiveDomainListsFor("first", []string{"work"}, routes); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatal("clearing assignment did not restore membership")
	}
}
