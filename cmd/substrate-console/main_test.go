package main

import (
	"strings"
	"testing"
)

func TestPublicURLNeedsAnIdentity(t *testing.T) {
	const pub = "https://console.example.com"
	for _, c := range []struct {
		name string
		f    flags
		ok   bool
	}{
		{"shared owner refused", flags{publicURL: pub}, false},
		{"service account", flags{publicURL: pub, sa: "ate-users/alice"}, true},
		{"token command", flags{publicURL: pub, tokenCommand: "true"}, true},
		{"forward token", flags{publicURL: pub, forward: true}, true},
		{"demo", flags{publicURL: pub, demo: true}, true},
		{"no public url", flags{}, true},
	} {
		err := checkIdentity("serve", c.f)
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), "--service-account")) {
			t.Errorf("%s: err = %v, want one naming --service-account", c.name, err)
		}
	}
}
