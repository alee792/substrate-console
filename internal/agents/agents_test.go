package agents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewChecksConfig(t *testing.T) {
	for _, c := range []Config{{}, {URL: "localhost:8000", APIKey: "k"}, {URL: "http://localhost:8000"}} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) took it", c)
		}
	}
	c, err := New(Config{URL: "https://canopy.example.com/", APIKey: "k"})
	if err != nil || c.Host() != "canopy.example.com" {
		t.Fatalf("%v %q", err, c.Host())
	}
}

// Canopy's error envelope becomes the message; a 404 is NotFound.
func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/agents/nope") {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"code":"not_found","message":"no agent nope"}}`))
			return
		}
		w.WriteHeader(502)
		w.Write([]byte(`<html>bad gateway</html>`))
	}))
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Agent(context.Background(), "nope", 0)
	if !NotFound(err) || err.Error() != "no agent nope" {
		t.Errorf("404: %v", err)
	}
	_, err = c.Agents(context.Background())
	if NotFound(err) || !strings.Contains(err.Error(), "502") {
		t.Errorf("502: %v", err)
	}
}

func TestGitHubRepo(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "",
		"github.com/alee792/canopy":          "https://github.com/alee792/canopy",
		"https://github.com/alee792/canopy/": "https://github.com/alee792/canopy",
	} {
		if got, err := GitHubRepo(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"http://github.com/a/b", "https://github.com", "not a url at all/"} {
		if _, err := GitHubRepo(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
