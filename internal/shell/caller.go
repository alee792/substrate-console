package shell

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/alee792/substrate-console/internal/backend/ate"
	"github.com/alee792/substrate-console/internal/mcpapp"
)

// forwardCaller makes /mcp act as each caller: it requires the caller's own
// ateapi bearer token and hands it to the backend, which sends it to
// ateapi. ateapi checks the token, and its access policies decide what the
// caller can see and do. The console checks nothing itself, so it accepts
// whatever identities ateapi trusts, OIDC or otherwise.
func forwardCaller(next http.Handler) http.Handler {
	return auth.RequireBearerToken(func(_ context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
		info := &auth.TokenInfo{Extra: map[string]any{mcpapp.TokenKey: tok}}
		// Unverified: the expiry only saves a round trip, and the subject
		// only keeps one caller's history apart from another's. ateapi
		// does the real checks.
		info.UserID, info.Expiration = ate.Claims(tok)
		if info.Expiration.IsZero() {
			info.Expiration = time.Now().Add(5 * time.Minute)
		}
		return info, nil
	}, &auth.RequireBearerTokenOptions{ClockSkew: time.Minute})(next)
}
