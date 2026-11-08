package mcpapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
				ctx = backend.WithCaller(ctx, backend.Caller{Token: tok})
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

// callerState is what the server keeps for one caller: their latest
// snapshot and the rules engine that remembers their resolved problems.
type callerState struct {
	engine   *rules.Engine
	cached   map[string]*model.Cluster // by requested atespace
	cacheAt  map[string]time.Time
	lastUsed time.Time
}

// callerID names the caller in ctx without keeping their token: a hash of
// it, or "" for the console's own identity.
func callerID(ctx context.Context) string {
	c, ok := backend.CallerFrom(ctx)
	if !ok {
		return ""
	}
	h := sha256.Sum256([]byte(c.Token))
	return hex.EncodeToString(h[:])
}

// state returns the caller's state, creating it, and drops callers idle
// for a day. Call with s.mu held.
func (s *Server) state(ctx context.Context) *callerState {
	id := callerID(ctx)
	now := time.Now()
	for k, st := range s.callers {
		if now.Sub(st.lastUsed) > 24*time.Hour {
			delete(s.callers, k)
		}
	}
	st := s.callers[id]
	if st == nil {
		st = &callerState{engine: rules.NewEngine(), cached: map[string]*model.Cluster{}, cacheAt: map[string]time.Time{}}
		s.callers[id] = st
	}
	st.lastUsed = now
	return st
}

func (s *Server) engineFor(ctx context.Context) *rules.Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state(ctx).engine
}

func (s *Server) snapshot(ctx context.Context) (*model.Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, key := s.state(ctx), backend.AtespaceFrom(ctx)
	if c := st.cached[key]; c != nil && time.Since(st.cacheAt[key]) < s.opts.CacheFor {
		return c, nil
	}
	c, err := s.opts.Backend.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cluster: %w", err)
	}
	st.cached[key], st.cacheAt[key] = c, time.Now()
	return c, nil
}

// invalidate drops every caller's snapshot, since a write shows up for
// everyone.
func (s *Server) invalidate() {
	s.mu.Lock()
	for _, st := range s.callers {
		clear(st.cached)
	}
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
