package mcpapp_test

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/mcpapp"
)

// TestMCPAppsHandshake checks what a host such as Claude Desktop sees before
// it decides to render a view: the extension in the server's capabilities,
// and every ui:// link resolving to a resource it can read, with the view
// MIME type and a CSP on both the listing and the content.
func TestMCPAppsHandshake(t *testing.T) {
	cs := connect(t, false)
	ctx := context.Background()

	ui, ok := cs.InitializeResult().Capabilities.Extensions[mcpapp.ExtensionID].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.extensions has no %s", mcpapp.ExtensionID)
	}
	if mt, _ := ui["mimeTypes"].([]any); !slices.Contains(mt, any(mcpapp.ViewMIME)) {
		t.Errorf("%s mimeTypes = %v, want %s", mcpapp.ExtensionID, ui["mimeTypes"], mcpapp.ViewMIME)
	}

	listed := map[string]*mcp.Resource{}
	rl, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rl.Resources {
		listed[r.URI] = r
	}

	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range tl.Tools {
		meta, _ := tool.Meta["ui"].(map[string]any)
		if meta == nil {
			t.Errorf("%s: no _meta.ui", tool.Name)
			continue
		}
		vis, _ := meta["visibility"].([]any)
		for _, v := range vis {
			if v != "model" && v != "app" {
				t.Errorf("%s: visibility %v", tool.Name, v)
			}
		}
		uri, _ := meta["resourceUri"].(string)
		if uri == "" {
			continue
		}
		if flat := tool.Meta["ui/resourceUri"]; flat != nil && flat != uri {
			t.Errorf("%s: ui/resourceUri %v disagrees with ui.resourceUri %s", tool.Name, flat, uri)
		}
		r := listed[uri]
		if r == nil {
			t.Errorf("%s: %s is not in resources/list", tool.Name, uri)
			continue
		}
		if r.MIMEType != mcpapp.ViewMIME {
			t.Errorf("%s listed as %q", uri, r.MIMEType)
		}
		if _, ok := r.Meta["ui"].(map[string]any)["csp"]; !ok {
			t.Errorf("%s: listing has no _meta.ui.csp", uri)
		}
		if seen[uri] {
			continue
		}
		seen[uri] = true
		rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("read %s: %v", uri, err)
		}
		if len(rr.Contents) != 1 {
			t.Fatalf("%s: %d contents", uri, len(rr.Contents))
		}
		c := rr.Contents[0]
		if c.URI != uri || c.MIMEType != mcpapp.ViewMIME || c.Text == "" {
			t.Errorf("%s content: uri %q, mime %q, %d bytes", uri, c.URI, c.MIMEType, len(c.Text))
		}
		if _, ok := c.Meta["ui"].(map[string]any)["csp"]; !ok {
			t.Errorf("%s: content has no _meta.ui.csp", uri)
		}
	}
	if !seen[mcpapp.ViewURI] || !seen[mcpapp.CardURI] {
		t.Errorf("tools link %v, want both views", seen)
	}
}
