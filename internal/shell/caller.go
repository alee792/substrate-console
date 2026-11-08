package shell

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/alee792/substrate-console/internal/backend/ate"
	"github.com/alee792/substrate-console/internal/mcpapp"
)

// CallerAuth makes /mcp act as each caller: it requires the caller's own
// bearer token and hands it to the backend, which sends it to ateapi.
// ateapi's access policies then decide what the caller can see and do.
type CallerAuth struct {
	// Verifier checks each token's issuer, audience, signature and expiry
	// before the console forwards it, as an OAuth resource server must.
	// Nil forwards any token and leaves every check to ateapi.
	Verifier *oidc.IDTokenVerifier
	// Resource is the public URL of /mcp. With Issuer, /mcp serves OAuth
	// protected resource metadata (RFC 9728), so MCP hosts can sign the
	// caller in with that issuer.
	Resource string
	// Issuer is the authorization server MCP hosts sign callers in with.
	Issuer string
	// Scopes are the scopes hosts should ask for, if the issuer needs any.
	Scopes []string
}

// NewOIDCVerifier discovers issuer and returns a verifier that accepts its
// tokens for audience. client may be nil.
func NewOIDCVerifier(ctx context.Context, issuer, audience string, client *http.Client) (*oidc.IDTokenVerifier, error) {
	if client != nil {
		ctx = oidc.ClientContext(ctx, client)
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", issuer, err)
	}
	// The provider keeps using this context to refresh keys, so it must
	// outlive ctx's cancellation; only the client matters.
	return p.VerifierContext(oidc.ClientContext(context.Background(), client), &oidc.Config{ClientID: audience}), nil
}

// metadataPath is where RFC 9728 puts the metadata for a resource at /mcp.
const metadataPath = "/.well-known/oauth-protected-resource/mcp"

func (a *CallerAuth) metadataURL() string {
	u, err := url.Parse(a.Resource)
	if err != nil {
		return ""
	}
	u.Path, u.RawQuery = metadataPath, ""
	return u.String()
}

func (a *CallerAuth) withMetadata() bool { return a.Resource != "" && a.Issuer != "" }

func (a *CallerAuth) verify(ctx context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
	info := &auth.TokenInfo{Extra: map[string]any{mcpapp.TokenKey: tok}}
	if a.Verifier != nil {
		id, err := a.Verifier.Verify(ctx, tok)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		info.UserID, info.Expiration = id.Subject, id.Expiry
		return info, nil
	}
	// Unverified: the expiry only saves a round trip, and the subject
	// only keeps one caller's session from being used with another's
	// token. ateapi does the real checks.
	info.UserID, info.Expiration = ate.Claims(tok)
	if info.Expiration.IsZero() {
		info.Expiration = time.Now().Add(5 * time.Minute)
	}
	return info, nil
}

func (a *CallerAuth) middleware() func(http.Handler) http.Handler {
	opts := &auth.RequireBearerTokenOptions{Scopes: a.Scopes, ClockSkew: time.Minute}
	if a.withMetadata() {
		opts.ResourceMetadataURL = a.metadataURL()
	}
	return auth.RequireBearerToken(a.verify, opts)
}

func (a *CallerAuth) metadata() http.Handler {
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               a.Resource,
		AuthorizationServers:   []string{a.Issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        a.Scopes,
		ResourceName:           "Substrate console",
	})
}
