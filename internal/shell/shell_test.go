package shell

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToken(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	const tok = "0123456789abcdef0123"
	h := Handler(srv, []byte("<html>"), Auth{Token: tok})
	cases := []struct {
		name, target, auth, origin string
		want                       int
	}{
		{"page needs no token", "/", "", "", http.StatusOK},
		{"no token", "/mcp", "", "", http.StatusUnauthorized},
		{"wrong token", "/mcp", "Bearer nope", "", http.StatusUnauthorized},
		{"wrong header beats right query", "/mcp?key=" + tok, "Bearer nope", "", http.StatusUnauthorized},
		{"header", "/mcp", "Bearer " + tok, "", 0},
		{"query", "/mcp?key=" + tok, "", "", 0},
		{"other origin", "/mcp", "Bearer " + tok, "https://evil.example", http.StatusForbidden},
		{"Sec-Fetch-Site cross-site", "/mcp", "Bearer " + tok, "fetch:cross-site", http.StatusForbidden},
	}
	for _, c := range cases {
		method := http.MethodGet
		if c.origin != "" {
			method = http.MethodPost // cross-origin protection leaves safe methods alone
		}
		r := httptest.NewRequest(method, c.target, nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if site, ok := strings.CutPrefix(c.origin, "fetch:"); ok {
			r.Header.Set("Sec-Fetch-Site", site)
		} else if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		switch {
		case c.want == 0 && (w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden):
			t.Errorf("%s: got %d, want the request let through", c.name, w.Code)
		case c.want != 0 && w.Code != c.want:
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestNoToken(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	w := httptest.NewRecorder()
	Handler(srv, nil, Auth{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if w.Code == http.StatusUnauthorized {
		t.Fatal("/mcp asked for a token with auth off")
	}
}

// TestPublicHost serves the handler on a real loopback listener, since the
// SDK's DNS rebinding check reads the listener's address.
func TestPublicHost(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	const tok = "0123456789abcdef0123"
	for _, c := range []struct {
		name, public, host string
		want               int
	}{
		{"tunnel host refused without a public host", "", "console.example.com", http.StatusForbidden},
		{"public host", "console.example.com", "console.example.com", 0},
		{"public host with port", "console.example.com", "Console.Example.com:443", 0},
		{"loopback still works", "console.example.com", "127.0.0.1:7878", 0},
		{"other host refused", "console.example.com", "evil.example", http.StatusForbidden},
	} {
		ts := httptest.NewServer(Handler(srv, nil, Auth{Token: tok, PublicHost: c.public}))
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
		r, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp?key="+tok, strings.NewReader(body))
		r.Host = c.host
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(r)
		ts.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		switch {
		case c.want == 0 && resp.StatusCode != http.StatusOK:
			t.Errorf("%s: got %d, want 200", c.name, resp.StatusCode)
		case c.want != 0 && resp.StatusCode != c.want:
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
}
