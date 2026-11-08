package mcpapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
	"github.com/alee792/substrate-console/internal/rules"
)

// TokenKey is where a TokenVerifier puts the caller's raw bearer token in
// auth.TokenInfo.Extra, for the console to forward to ateapi.
const TokenKey = "ateapi_token"

// withCaller moves the caller's token from the HTTP request into the
// context the backend reads, and the atespace a tool call names, so that a
// caller who can't list atespaces still gets that one.
func withCaller(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if ex := req.GetExtra(); ex != nil && ex.TokenInfo != nil {
			if tok, _ := ex.TokenInfo.Extra[TokenKey].(string); tok != "" {
				ctx = backend.WithCaller(ctx, backend.Caller{Token: tok, Principal: ex.TokenInfo.UserID})
			}
		}
		if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
			var args struct {
				Atespace string `json:"atespace"`
			}
			if json.Unmarshal(call.Params.Arguments, &args) == nil {
				ctx = backend.WithAtespace(ctx, args.Atespace)
			}
		}
		return next(ctx, method, req)
	}
}

// The server keeps two things per caller, keyed differently:
//
//   - Snapshots, by a hash of the token. A snapshot is what that token was
//     allowed to read, so only the same token may reuse it. Tokens rotate,
//     so entries older than CacheFor are swept on every read.
//   - The rules engine that remembers resolved problems, by principal, so
//     history survives token rotation. The principal may be unverified (the
//     token's own sub, in forward mode without an issuer), but it is only
//     reached after the snapshot, read with the same token, succeeded:
//     ateapi has accepted the token, so its sub is genuine.
type snap struct {
	c  *model.Cluster
	at time.Time
}

type engineState struct {
	engine   *rules.Engine
	lastUsed time.Time
}

// tokenKey names the token in ctx without keeping it: a hash, or "" for
// the console's own identity.
func tokenKey(ctx context.Context) string {
	c, ok := backend.CallerFrom(ctx)
	if !ok {
		return ""
	}
	h := sha256.Sum256([]byte(c.Token))
	return hex.EncodeToString(h[:])
}

func principalKey(ctx context.Context) string {
	c, ok := backend.CallerFrom(ctx)
	if !ok {
		return ""
	}
	if c.Principal == "" {
		return "token:" + tokenKey(ctx)
	}
	return "sub:" + c.Principal
}

// engineFor returns the caller's rules engine. Engines idle for a day, the
// span resolved problems are kept, are dropped.
func (s *Server) engineFor(ctx context.Context) *rules.Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.engines {
		if now.Sub(e.lastUsed) > 24*time.Hour {
			delete(s.engines, k)
		}
	}
	k := principalKey(ctx)
	e := s.engines[k]
	if e == nil {
		e = &engineState{engine: rules.NewEngine()}
		s.engines[k] = e
	}
	e.lastUsed = now
	return e.engine
}

func (s *Server) snapshot(ctx context.Context) (*model.Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, sn := range s.snaps {
		if now.Sub(sn.at) >= s.opts.CacheFor {
			delete(s.snaps, k)
		}
	}
	key := tokenKey(ctx) + "|" + backend.AtespaceFrom(ctx)
	if sn, ok := s.snaps[key]; ok {
		return sn.c, nil
	}
	c, err := s.opts.Backend.Snapshot(ctx)
	if err != nil {
		if status.Code(err) == codes.Unauthenticated {
			return nil, errors.New("ateapi didn't accept your token; it may have expired")
		}
		return nil, fmt.Errorf("read cluster: %w", err)
	}
	if s.opts.CacheFor > 0 {
		s.snaps[key] = snap{c: c, at: time.Now()}
	}
	return c, nil
}

// invalidate drops every snapshot, since a write shows up for everyone.
func (s *Server) invalidate() {
	s.mu.Lock()
	clear(s.snaps)
	s.mu.Unlock()
}

// metricsFor returns the metrics a caller may see. Prometheus is read with
// the console's own access, so for a caller who isn't a global viewer it
// answers only for atespaces the caller can read, and never pool-wide.
func (s *Server) metricsFor(c *model.Cluster) backend.Metrics {
	if s.opts.Metrics == nil {
		return nil
	}
	if !c.Access.Limited() {
		return s.opts.Metrics
	}
	return scopedMetrics{m: s.opts.Metrics, atespaces: c.Atespaces}
}

type scopedMetrics struct {
	m         backend.Metrics
	atespaces []string
}

// errHidden is returned for metrics a caller can't see.
var errHidden = fmt.Errorf("%w: this metric covers more of the cluster than you can see", backend.ErrUnsupported)

func (sm scopedMetrics) Query(ctx context.Context, q backend.MetricQuery) ([]model.Series, error) {
	if q.Metric == backend.MetricCapacity || q.By == "pool" {
		return nil, errHidden
	}
	if q.Atespace != "" {
		if !slices.Contains(sm.atespaces, q.Atespace) {
			return nil, errHidden
		}
		return sm.m.Query(ctx, q)
	}
	// Per template, the series of each visible atespace add up to the
	// caller's whole view. A single total across atespaces can't be built
	// from per-atespace series for every metric, so it isn't offered.
	if q.By != "template" {
		if len(sm.atespaces) == 1 {
			q.Atespace = sm.atespaces[0]
			return sm.m.Query(ctx, q)
		}
		return nil, errHidden
	}
	var out []model.Series
	for _, a := range sm.atespaces {
		q.Atespace = a
		s, err := sm.m.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
	return out, nil
}
