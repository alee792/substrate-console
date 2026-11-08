package mcpapp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	substratefake "github.com/alee792/substrate-fake"

	"github.com/alee792/substrate-console/internal/backend/ate"
	"github.com/alee792/substrate-console/internal/mcpapp"
	"github.com/alee792/substrate-console/internal/metrics"
)

// connect serves the console against the fake demo cluster, through the
// live backend: ateapi over TLS, the Kubernetes API and Prometheus.
func connect(t *testing.T, readOnly bool) *mcp.ClientSession {
	t.Helper()
	cs, _ := connectFake(t, readOnly)
	return cs
}

func connectFake(t *testing.T, readOnly bool) (*mcp.ClientSession, *substratefake.Cluster) {
	t.Helper()
	ctx := context.Background()
	fc, err := substratefake.Start(ctx, substratefake.Options{Scenario: substratefake.Demo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	live, err := ate.New(ctx, ate.Config{Kubeconfig: fc.Kubeconfig, Context: fc.Context, Endpoint: fc.APIEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	s := mcpapp.New(mcpapp.Options{Backend: live, Metrics: metrics.NewPrometheus(fc.PrometheusURL), ViewHTML: []byte("<!doctype html><title>v</title>"), ReadOnly: readOnly, CacheFor: -1})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, fc
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
			if ui["resourceUri"] != mcpapp.CardURI || tool.Meta["openai/outputTemplate"] != mcpapp.CardURI {
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

func TestOpenAIEntrypoints(t *testing.T) {
	cs := connect(t, true)
	res, _ := cs.ListTools(context.Background(), nil)
	kinds := map[string]string{}
	for _, tool := range res.Tools {
		oui, _ := tool.Meta["openai/ui"].(map[string]any)
		eps, _ := oui["entrypoints"].([]any)
		for _, e := range eps {
			kinds[e.(map[string]any)["type"].(string)] = tool.Name
			ui := tool.Meta["ui"].(map[string]any)
			if ui["resourceUri"] != mcpapp.ViewURI {
				t.Errorf("%s: entrypoint must open the full view", tool.Name)
			}
			if tool.Title == "" || len(tool.Icons) == 0 {
				t.Errorf("%s: entrypoint needs a title and icons", tool.Name)
			}
		}
	}
	if kinds["global"] == "" || kinds["thread"] == "" || kinds["global"] == kinds["thread"] {
		t.Errorf("want separate global and thread entrypoint tools, got %v", kinds)
	}
	// Entrypoints are opened with {}.
	st, _ := call(t, cs, kinds["global"], map[string]any{})
	if st["kind"] != "status" {
		t.Errorf("global entrypoint kind = %v", st["kind"])
	}

	for uri, want := range map[string]string{mcpapp.ViewURI: "fullscreen", mcpapp.CardURI: "inline"} {
		r, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatal(err)
		}
		oui := r.Contents[0].Meta["openai/ui"].(map[string]any)
		if oui["preferredDisplayMode"] != want {
			t.Errorf("%s preferredDisplayMode = %v, want %s", uri, oui["preferredDisplayMode"], want)
		}
	}
}

func TestMentions(t *testing.T) {
	cs := connect(t, true)
	ext := cs.InitializeResult().Capabilities.Extensions["openai/mentions"].(map[string]any)
	tool := ext["searchTool"].(string)
	res, _ := call(t, cs, tool, map[string]any{"query": "triage-0d55"})
	items := res["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	it := items[0].(map[string]any)
	if it["type"] != "resource_link" || it["uri"] != "substrate://actors/triage/triage-0d55" {
		t.Errorf("item = %v", it)
	}
	r, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: it["uri"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Contents[0].Text, "triage/triage-0d55") {
		t.Errorf("actor resource = %q", r.Contents[0].Text)
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
	for _, want := range []string{"3 crashes on node kind-worker3", "mcp-enrich-v4 golden snapshot failed", "ioc-enrich-02 stuck resuming", "gvisor-pool-a full, resumes failing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("status items %q lack %q", joined, want)
		}
	}
	if !strings.HasPrefix(txt, "cluster demo-cluster: 4 issues") {
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
	// A crashed actor has no logs to read, so the hint comes from the crash
	// message and the template's newer version.
	if !strings.Contains(hint, "Hit the 2 GiB memory limit.") || !strings.Contains(hint, "agent-devbox-v12 allows 4 GiB") {
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
	if r["summary"] != "Revert 1 actor? 1 skipped." {
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

func TestTemplateVersionFlow(t *testing.T) {
	cs := connect(t, false)
	d, _ := call(t, cs, "get_template", map[string]any{"atespace": "triage", "name": "agent-devbox-v11"})
	tm := d["template"].(map[string]any)
	if tm["family"] != "agent-devbox" || tm["sandboxClass"] != "gvisor" || tm["crashed"].(float64) != 3 {
		t.Errorf("template = %v", tm)
	}
	if n := len(d["versions"].([]any)); n != 2 {
		t.Errorf("versions = %d, want 2", n)
	}

	// New version: the draft is filled from the source and named v13,
	// because v12 is taken.
	dr, _ := call(t, cs, "create_template", map[string]any{"atespace": "triage", "from": "agent-devbox-v11", "dry_run": true})
	spec := dr["spec"].(map[string]any)
	if spec["name"] != "agent-devbox-v13" || spec["image"] != "ghcr.io/example/agent-devbox:11" || spec["pool"] != "gvisor-pool-a" || spec["golden"] != true {
		t.Errorf("draft spec = %v", spec)
	}
	if len(dr["problems"].(map[string]any)) != 0 {
		t.Errorf("problems = %v", dr["problems"])
	}

	bad, _ := call(t, cs, "create_template", map[string]any{"atespace": "triage", "name": "agent-devbox-v12", "image": "x", "cpuMillicores": 1000, "memoryMiB": 1024, "dry_run": true})
	if bad["problems"].(map[string]any)["name"] == nil {
		t.Error("taken name not flagged")
	}

	spec["image"] = "ghcr.io/example/agent-devbox:13"
	spec["memoryMiB"] = 4096
	created, _ := call(t, cs, "create_template", spec)
	if created["kind"] != "template" || created["template"].(map[string]any)["golden"].(map[string]any)["state"] != "building" {
		t.Errorf("created = %v", created["template"])
	}

	a, _ := call(t, cs, "create_actor", map[string]any{"atespace": "triage", "template": "agent-devbox-v13", "name": "triage-new1"})
	if a["summary"] != "Created triage-new1." {
		t.Errorf("create_actor = %v", a["summary"])
	}
	got, _ := call(t, cs, "get_actor", map[string]any{"atespace": "triage", "name": "triage-new1"})
	if got["actor"].(map[string]any)["state"] != "suspended" {
		t.Errorf("new actor = %v", got["actor"])
	}
}

// A worker's log holds whatever ran there before; an actor's logs start
// when it arrived.
func TestActorLogsLeaveOutTheWorkersEarlierActors(t *testing.T) {
	cs, fc := connectFake(t, false)
	// A later write, such as a snapshot report, moves update_time but not
	// when the actor arrived.
	fc.World.Touch("triage", "triage-7f3a")
	d, _ := call(t, cs, "get_actor", map[string]any{"atespace": "triage", "name": "triage-7f3a"})
	b, _ := json.Marshal(d["logs"])
	if !strings.Contains(string(b), "waiting for input") || strings.Contains(string(b), "212 alerts") {
		t.Errorf("logs = %s", b)
	}
	if d["logsSince"] == nil {
		t.Error("no logsSince")
	}
	_, txt := call(t, cs, "actor_logs", map[string]any{"atespace": "triage", "name": "triage-7f3a"})
	if strings.Contains(txt, "212 alerts") || !strings.Contains(txt, "Earlier lines belong to whatever ran there before") {
		t.Errorf("actor_logs text = %q", txt)
	}
}
