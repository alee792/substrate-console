package mcpapp

// OpenAI MCP Extensions (github.com/openai/mcp-extensions, docs/spec.md):
// sidebar and thread entrypoints, display-mode hints on the view resources
// (see viewMeta), and composer @-mentions for actors and atespaces. They are
// plain _meta keys and capabilities, so other hosts ignore them.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/model"
	"github.com/alee792/substrate-console/internal/rules"
)

const (
	mentionTool = "search_mentions"
	actorScheme = "substrate://actors/"
	spaceScheme = "substrate://atespaces/"
)

// The entrypoint icon: three stacked layers, one stroke color, 20×20 at
// 1.33px, as the extension's icon guidance asks.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 20 20" fill="none" stroke="%s" stroke-width="1.33" stroke-linejoin="round"><path d="M10 3 17 6.5 10 10 3 6.5Z"/><path d="M3 10 10 13.5 17 10"/><path d="M3 13.5 10 17 17 13.5"/></svg>`

var icons = []mcp.Icon{
	{Source: svgData(fmt.Sprintf(iconSVG, "#0d0d0d")), MIMEType: "image/svg+xml", Sizes: []string{"any"}, Theme: "light"},
	{Source: svgData(fmt.Sprintf(iconSVG, "#ffffff")), MIMEType: "image/svg+xml", Sizes: []string{"any"}, Theme: "dark"},
}

func svgData(svg string) string {
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

// capabilities declares the extensions the server uses. MCP Apps is
// negotiated through the extensions capability (SEP-1724), and Claude
// Desktop has been seen to show views only from servers that declare it,
// so the server lists it with the one view type it serves. The OpenAI
// @-mention search tool is read from capabilities["extensions"], or from
// "experimental" on protocol 2025-11-25 and earlier, so it goes in both.
func capabilities() *mcp.ServerCapabilities {
	mentions := map[string]any{"searchTool": mentionTool}
	return &mcp.ServerCapabilities{
		Extensions: map[string]any{
			ExtensionID:       map[string]any{"mimeTypes": []string{ViewMIME}},
			"openai/mentions": mentions,
		},
		Experimental: map[string]any{"openai/mentions": mentions},
	}
}

// EntryIn is what an entrypoint receives. Hosts send {} when the user opens
// it from the sidebar or a thread tab; the view navigates from there.
type EntryIn struct {
	Atespace string `json:"atespace,omitempty" jsonschema:"open scoped to one atespace"`
}

// addEntrypoints registers one tool per entrypoint, as the extension's
// patterns recommend. Both are app-only: the model uses status instead.
func (s *Server) addEntrypoints(srv *mcp.Server) {
	for _, e := range []struct{ name, title, kind string }{
		{"open_console", "Substrate", "global"},
		{"console_tab", "Substrate", "thread"},
	} {
		meta := viewToolMeta(ViewURI, "Opening Substrate…", "Opened Substrate")
		meta["ui"] = map[string]any{"resourceUri": ViewURI, "visibility": []string{"app"}}
		meta["openai/ui"] = map[string]any{"entrypoints": []map[string]any{{"type": e.kind}}}
		mcp.AddTool(srv, &mcp.Tool{
			Name: e.name, Title: e.title, Icons: icons,
			Description: "Open the Substrate console on the cluster overview.",
			Annotations: readOnly(e.title),
			Meta:        meta,
		}, func(ctx context.Context, req *mcp.CallToolRequest, in EntryIn) (*mcp.CallToolResult, any, error) {
			return s.status(ctx, req, ScopeIn(in))
		})
	}
}

// MentionIn is the @-mention search query; it may be empty.
type MentionIn struct {
	Query string `json:"query" jsonschema:"search text, may be empty"`
}

// MentionResult is the shape the extension asks for.
type MentionResult struct {
	Items []resourceLink `json:"items"`
}

type resourceLink struct {
	Type        string `json:"type"` // "resource_link"
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

func (s *Server) addMentions(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        mentionTool,
		Description: "Search actors and atespaces by name for @-mentions in the composer.",
		Annotations: readOnly("Search actors"),
		Meta:        mcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}},
	}, s.searchMentions)

	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: actorScheme + "{atespace}/{name}", Name: "actor", Title: "Actor",
		Description: "One actor: state, crash reason, worker, recent logs.", MIMEType: "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		rest := strings.TrimPrefix(req.Params.URI, actorScheme)
		ns, name, ok := strings.Cut(rest, "/")
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		ns, _ = url.PathUnescape(ns)
		name, _ = url.PathUnescape(name)
		res, _, err := s.getActor(ctx, nil, ActorIn{Atespace: ns, Name: name})
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		return textResource(req.Params.URI, res), nil
	})

	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: spaceScheme + "{atespace}", Name: "atespace", Title: "Atespace",
		Description: "What needs attention in one atespace, and its vitals.", MIMEType: "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		ns, _ := url.PathUnescape(strings.TrimPrefix(req.Params.URI, spaceScheme))
		res, _, err := s.status(ctx, nil, ScopeIn{Atespace: ns})
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		return textResource(req.Params.URI, res), nil
	})
}

func textResource(uri string, res *mcp.CallToolResult) *mcp.ReadResourceResult {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/plain", Text: b.String()}}}
}

func (s *Server) searchMentions(ctx context.Context, _ *mcp.CallToolRequest, in MentionIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	out := MentionResult{Items: []resourceLink{}}
	for _, ns := range c.Atespaces {
		if q == "" || strings.Contains(ns, q) {
			out.Items = append(out.Items, resourceLink{Type: "resource_link", URI: spaceScheme + url.PathEscape(ns), Name: ns, Title: ns, Description: "Atespace", MIMEType: "text/plain"})
		}
	}
	type hit struct {
		a    model.Actor
		rank int
	}
	var hits []hit
	for _, a := range c.Actors {
		id := strings.ToLower(a.Atespace + "/" + a.Name)
		if q != "" && !strings.Contains(id, q) {
			continue
		}
		r := 2
		if ny, _ := needsYou(a, c.FetchedAt); ny {
			r = 0
		} else if a.State == model.StateRunning {
			r = 1
		}
		hits = append(hits, hit{a, r})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].a.Name < hits[j].a.Name
	})
	for i, h := range hits {
		if i == 20 {
			break
		}
		desc := string(h.a.State) + " · " + h.a.Atespace
		if h.a.State == model.StateCrashed && h.a.Crash != nil {
			desc = "crashed, " + strings.ToLower(rules.CrashReason(h.a.Crash.Message)) + " · " + h.a.Atespace
		}
		out.Items = append(out.Items, resourceLink{
			Type: "resource_link", URI: actorScheme + url.PathEscape(h.a.Atespace) + "/" + url.PathEscape(h.a.Name),
			Name: h.a.Name, Title: h.a.Name, Description: desc, MIMEType: "text/plain",
		})
	}
	return &mcp.CallToolResult{Content: []mcp.Content{}}, out, nil
}
