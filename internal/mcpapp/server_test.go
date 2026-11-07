package mcpapp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/backend/demo"
	"github.com/alee792/substrate-console/internal/mcpapp"
)

var now = time.Date(2026, 11, 7, 16, 8, 41, 0, time.UTC)

func connect(t *testing.T, readOnly bool) *mcp.ClientSession {
	t.Helper()
	d := demo.New(func() time.Time { return now })
	s := mcpapp.New(mcpapp.Options{Backend: d, Metrics: d, ViewHTML: []byte("<!doctype html><title>v</title>"), ReadOnly: readOnly, CacheFor: -1})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var txt string
	if len(res.Content) > 0 {
		txt = res.Content[0].(*mcp.TextContent).Text
	}
	if res.IsError {
		t.Fatalf("%s returned error: %s", name, txt)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s structured content: %v", name, err)
	}
	return m, txt
}

func TestToolsDeclareTheView(t *testing.T) {
	cs := connect(t, false)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	withView := map[string]bool{"status": true, "list_actors": true, "get_actor": true, "query_metric": true, "revert_actors": true, "drain_worker": true}
	seen := map[string]bool{}
	for _, tool := range res.Tools {
		seen[tool.Name] = true
		if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || tool.Annotations.DestructiveHint == nil {
			t.Errorf("%s: readOnlyHint, destructiveHint and openWorldHint must all be set", tool.Name)
		}
		ui, _ := tool.Meta["ui"].(map[string]any)
		if ui == nil {
			t.Errorf("%s: no _meta.ui", tool.Name)
			continue
		}
		if withView[tool.Name] {
			if ui["resourceUri"] != mcpapp.ViewURI || tool.Meta["openai/outputTemplate"] != mcpapp.ViewURI {
				t.Errorf("%s: view not linked: %v", tool.Name, tool.Meta)
			}
		}
	}
	for name := range withView {
		if !seen[name] {
			t.Errorf("tool %s missing", name)
		}
	}
	if seen["delete_actors"] != true {
		t.Error("write tools missing")
	}
}

func TestReadOnlyHasNoWriteTools(t *testing.T) {
	cs := connect(t, true)
	res, _ := cs.ListTools(context.Background(), nil)
	for _, tool := range res.Tools {
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("read-only server exposes %s", tool.Name)
		}
	}
}

func TestViewResource(t *testing.T) {
	cs := connect(t, false)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: mcpapp.ViewURI})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Contents[0]
	if c.MIMEType != "text/html;profile=mcp-app" {
		t.Errorf("mimeType = %q", c.MIMEType)
	}
	if _, ok := c.Meta["ui"].(map[string]any)["csp"]; !ok {
		t.Error("resource has no _meta.ui.csp")
	}
	if c.Meta["openai/widgetCSP"] == nil {
		t.Error("resource has no openai/widgetCSP alias")
	}
}

func TestStatusFindsTheDemoProblems(t *testing.T) {
	cs := connect(t, false)
	st, txt := call(t, cs, "status", nil)
	if st["kind"] != "status" {
		t.Fatalf("kind = %v", st["kind"])
	}
	items := st["items"].([]any)
	var titles []string
	for _, it := range items {
		titles = append(titles, it.(map[string]any)["title"].(string))
	}
	joined := strings.Join(titles, "|")
	for _, want := range []string{"3 actors crashed on worker w-03", "ioc-enrich-02 has been resuming for 9 minutes", "4.1% of resumes failed for lack of capacity"} {
		if !strings.Contains(joined, want) {
			t.Errorf("status items %q lack %q", joined, want)
		}
	}
	if !strings.HasPrefix(txt, "cluster kind-sec-tools: 3 things need you") {
		t.Errorf("text = %q", txt)
	}

	scoped, _ := call(t, cs, "status", map[string]any{"atespace": "code-review"})
	for _, it := range scoped["items"].([]any) {
		if strings.Contains(it.(map[string]any)["title"].(string), "crashed") {
			t.Errorf("code-review scope shows triage crash: %v", it)
		}
	}
}

func TestGetActorExplainsTheCrash(t *testing.T) {
	cs := connect(t, false)
	d, txt := call(t, cs, "get_actor", map[string]any{"atespace": "triage", "name": "triage-0d55"})
	hint, _ := d["hint"].(string)
	if !strings.Contains(hint, "4 GiB heap inside a 2 GiB sandbox") {
		t.Errorf("hint = %q", hint)
	}
	if !strings.Contains(txt, "is crashed") {
		t.Errorf("text = %q", txt)
	}
}

func TestRevertDryRunThenApply(t *testing.T) {
	cs := connect(t, false)
	args := map[string]any{"actors": []map[string]string{{"atespace": "triage", "name": "triage-0d55"}, {"atespace": "triage", "name": "triage-7f3a"}}, "dry_run": true}
	r, _ := call(t, cs, "revert_actors", args)
	if r["summary"] != "This would revert 1 actor. 1 actor would be skipped." {
		t.Errorf("dry run summary = %v", r["summary"])
	}
	before, _ := call(t, cs, "get_actor", map[string]any{"atespace": "triage", "name": "triage-0d55"})
	if before["actor"].(map[string]any)["state"] != "crashed" {
		t.Fatal("dry run changed the actor")
	}
	args["dry_run"] = false
	call(t, cs, "revert_actors", args)
	after, _ := call(t, cs, "get_actor", map[string]any{"atespace": "triage", "name": "triage-0d55"})
	if got := after["actor"].(map[string]any)["state"]; got != "suspended" {
		t.Errorf("after revert state = %v", got)
	}
}

func TestQueryMetric(t *testing.T) {
	cs := connect(t, false)
	m, _ := call(t, cs, "query_metric", map[string]any{"metric": "resume_p95", "by": "template", "atespace": "triage"})
	if n := len(m["series"].([]any)); n != 2 {
		t.Errorf("triage templates = %d series, want 2", n)
	}
}
