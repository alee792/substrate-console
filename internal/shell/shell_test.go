package shell

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToken(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	const tok = "0123456789abcdef0123"
	h := Handler(srv, []byte("<html>"), tok)
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
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.target, nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if c.origin != "" {
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
	Handler(srv, nil, "").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if w.Code == http.StatusUnauthorized {
		t.Fatal("/mcp asked for a token with auth off")
	}
}
