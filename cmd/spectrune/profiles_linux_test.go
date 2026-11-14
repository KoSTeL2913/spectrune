package main

import "testing"

func TestLocalizedProfileAndListNames(t *testing.T) {
	for _, name := range []string{"Москва", "дс", "Профиль 2", "東京", "work-vpn"} {
		if !profileNameIsValid(name) {
			t.Errorf("valid localized name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "../secret", "bad/name", "bad\\name", "line\nbreak"} {
		if profileNameIsValid(name) {
			t.Errorf("unsafe name accepted: %q", name)
		}
	}
}
