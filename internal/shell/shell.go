// Package shell serves `substrate-console serve`: the MCP endpoint over
// Streamable HTTP at /mcp, and at / a small MCP Apps host page that renders
// the same view the MCP hosts render.
package shell

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handler returns the HTTP handler for the shell and the MCP endpoint. When
// token is set, /mcp requires it (see requireToken); the page at / stays
// open, because it holds no data and asks for the token itself.
func Handler(server *mcp.Server, shellHTML []byte, token string) http.Handler {
	mux := http.NewServeMux()
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	if token != "" {
		mcpHandler = requireToken(token, mcpHandler)
	}
	mux.Handle("/mcp", sameOrigin(mcpHandler))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(shellHTML)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	return mux
}

// requireToken accepts "Authorization: Bearer <token>", or ?key=<token> for
// hosts whose connector settings take only a URL. The query form ends up in
// access logs, so prefer the header where the host allows it.
func requireToken(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("key")
		if h := r.Header.Get("Authorization"); h != "" {
			got, _ = strings.CutPrefix(h, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="substrate-console"`)
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects browser requests from other origins, so a web page the
// user visits cannot drive the console's write tools through their browser.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !sameHost(o, r.Host) {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameHost(origin, host string) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return o == host
}
