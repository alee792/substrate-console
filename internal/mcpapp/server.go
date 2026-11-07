// Package mcpapp is the console's MCP server. It is an MCP App: its tools
// carry a ui:// view under the MCP Apps extension (io.modelcontextprotocol/ui,
// spec 2026-01-26), with OpenAI's Apps SDK aliases alongside so ChatGPT
// renders the same view. Every tool also returns plain text, so hosts that
// don't render views (Claude Code, Codex today) still get the answer.
package mcpapp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
	"github.com/alee792/substrate-console/internal/rules"
)

// MCP Apps constants (SEP-1865, stable 2026-01-26).
const (
	ExtensionID = "io.modelcontextprotocol/ui"
	ViewMIME    = "text/html;profile=mcp-app"
	ViewURI     = "ui://substrate-console/console.html"
)

// Options configures the server.
type Options struct {
	Backend backend.Backend
	// Metrics may be nil when no Prometheus is configured.
	Metrics backend.Metrics
	// ViewHTML is the bundled view served at ViewURI.
	ViewHTML []byte
	Version  string
	// ReadOnly leaves out every tool that changes the cluster.
	ReadOnly bool
	// FontDomains are origins the view loads fonts from. They are declared
	// in the view's CSP. Leave empty to use the host's fonts only.
	FontDomains []string
	// CacheFor bounds how often tools hit the backend. Default 2s.
	CacheFor time.Duration
}

// Server holds the state shared by every MCP session.
type Server struct {
	opts   Options
	engine *rules.Engine

	mu      sync.Mutex
	cached  *model.Cluster
	cacheAt time.Time
}

// New returns the shared server state. Call MCP to get an *mcp.Server.
func New(opts Options) *Server {
	if opts.CacheFor == 0 {
		opts.CacheFor = 2 * time.Second
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	return &Server{opts: opts, engine: rules.NewEngine()}
}

const instructions = `substrate-console reads and operates an Agent Substrate cluster.
Start with the status tool: it returns what needs attention and four vital signs, for the whole cluster or one atespace.
Use get_actor to explain a single actor (crash reason, timeline, logs). Use query_metric for ad-hoc charts instead of asking for dashboards.
Write tools (suspend, resume, revert, delete, drain) accept dry_run; call them with dry_run=true first and show the user which actors would change.`

// MCP builds an *mcp.Server with every tool and the view resource.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "substrate-console", Title: "Substrate Console", Version: s.opts.Version},
		&mcp.ServerOptions{Instructions: instructions})
	s.addView(srv)
	s.addReadTools(srv)
	if !s.opts.ReadOnly {
		s.addWriteTools(srv)
	}
	return srv
}

func (s *Server) snapshot(ctx context.Context) (*model.Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Since(s.cacheAt) < s.opts.CacheFor {
		return s.cached, nil
	}
	c, err := s.opts.Backend.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cluster: %w", err)
	}
	s.cached, s.cacheAt = c, time.Now()
	return c, nil
}

func (s *Server) invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

// viewMeta is the resource _meta: the MCP Apps fields under "ui", and the
// OpenAI Apps SDK aliases that ChatGPT still reads.
func (s *Server) viewMeta() mcp.Meta {
	domains := append([]string{}, s.opts.FontDomains...)
	return mcp.Meta{
		"ui": map[string]any{
			"csp": map[string]any{
				"connectDomains":  []string{},
				"resourceDomains": domains,
			},
			"prefersBorder": true,
		},
		"openai/widgetCSP": map[string]any{
			"connect_domains":  []string{},
			"resource_domains": domains,
		},
		"openai/widgetPrefersBorder": true,
		"openai/widgetDescription":   "Substrate console: what needs attention, vital signs, actors and charts for an Agent Substrate cluster.",
	}
}

func (s *Server) addView(srv *mcp.Server) {
	meta := s.viewMeta()
	srv.AddResource(&mcp.Resource{
		URI: ViewURI, Name: "console", Title: "Substrate Console",
		Description: "Interactive view for substrate-console tool results.",
		MIMEType:    ViewMIME, Meta: meta,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: ViewURI, MIMEType: ViewMIME, Text: string(s.opts.ViewHTML), Meta: meta,
		}}}, nil
	})
}

// toolMeta links a tool to the view. "ui" is the MCP Apps field;
// "ui/resourceUri" is the spec's deprecated flat key, kept for hosts built
// before it moved; the openai/* keys are the Apps SDK aliases.
func toolMeta(invoking, invoked string) mcp.Meta {
	return mcp.Meta{
		"ui":                             map[string]any{"resourceUri": ViewURI, "visibility": []string{"model", "app"}},
		"ui/resourceUri":                 ViewURI,
		"openai/outputTemplate":          ViewURI,
		"openai/widgetAccessible":        true,
		"openai/toolInvocation/invoking": invoking,
		"openai/toolInvocation/invoked":  invoked,
	}
}

// textToolMeta is for tools with no view of their own.
func textToolMeta(invoking, invoked string) mcp.Meta {
	return mcp.Meta{
		"ui":                             map[string]any{"visibility": []string{"model", "app"}},
		"openai/widgetAccessible":        true,
		"openai/toolInvocation/invoking": invoking,
		"openai/toolInvocation/invoked":  invoked,
	}
}

func ptr[T any](v T) *T { return &v }

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
}

func write(title string, destructive, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: ptr(destructive), IdempotentHint: idempotent, OpenWorldHint: ptr(false)}
}

func text(s string) []mcp.Content { return []mcp.Content{&mcp.TextContent{Text: s}} }
