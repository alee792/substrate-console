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

// Auth says what /mcp requires. The zero value requires nothing.
type Auth struct {
	// Token is a shared secret /mcp requires (see requireToken). Everyone
	// who has it acts as the console's own identity.
	Token string
	// Forward makes /mcp act as each caller instead: it requires their own
	// ateapi token and sends it on. Token is then ignored.
	Forward bool
}

// Handler returns the HTTP handler for the shell and the MCP endpoint. The
// page at / stays open, because it holds no data and asks for the token
// itself.
func Handler(server *mcp.Server, shellHTML []byte, a Auth) http.Handler {
	mux := http.NewServeMux()
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	switch {
	case a.Forward:
		mcpHandler = forwardCaller(mcpHandler)
	case a.Token != "":
		mcpHandler = requireToken(a.Token, mcpHandler)
	}
	// Browsers on other origins can't drive the write tools through the
	// user's browser. Requests without browser headers (MCP hosts, curl)
	// pass. DNS rebinding against a loopback listener is handled inside
	// the SDK's handler, which rejects non-loopback Host headers; keep its
	// DisableLocalhostProtection off.
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(mcpHandler))
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
